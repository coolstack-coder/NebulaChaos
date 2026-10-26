package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coolstack-coder/nebulaos/internal/raft"

	"github.com/coolstack-coder/nebulaos/internal/state"
)

// A tiny replicated key/value register. It exists so an external tester can
// check the cluster for linearizability: writes go through exactly the same
// Submit path as every other command. Reads are linearizable (see
// LinearizableRead); before that fix they were served from the local
// replica, like GET /api/services, and could be stale.
func (n *Node) kvRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/kv/{key}", n.handleKVGet)
	mux.HandleFunc("PUT /api/kv/{key}", n.handleKVPut)
	mux.HandleFunc("POST /api/kv/{key}/cas", n.handleKVCAS)
}

func (n *Node) handleKVGet(w http.ResponseWriter, r *http.Request) {
	if n.fsm == nil {
		writeErr(w, http.StatusNotFound, errors.New("not a control-plane node"))
		return
	}
	var (
		v  string
		ok bool
	)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	err := n.LinearizableRead(ctx, func(s *state.Snapshot) { v, ok = s.KV[r.PathValue("key")] })
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": v, "found": ok, "served_by": n.cfg.ID})
}

func (n *Node) handleKVPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	n.submitHTTP(w, r, state.Command{Op: state.OpKVPut, Key: r.PathValue("key"), Value: &body.Value}, http.StatusOK,
		map[string]string{"value": body.Value})
}

func (n *Node) handleKVCAS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Expect string `json:"expect"`
		Value  string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	n.submitHTTP(w, r, state.Command{Op: state.OpKVCAS, Key: r.PathValue("key"), Expect: &body.Expect, Value: &body.Value},
		http.StatusOK, map[string]string{"value": body.Value})
}

// LinearizableRead runs fn against a state that includes every write
// acknowledged before the call began. The leader uses Raft's ReadIndex
// (no log write); a follower appends a no-op read barrier through the
// leader and reads once it has applied it locally.
func (n *Node) LinearizableRead(ctx context.Context, fn func(s *state.Snapshot)) error {
	if n.raft == nil {
		return errors.New("not a control-plane node")
	}
	if n.raft.IsLeader() {
		_, err := n.raft.ReadIndex(ctx)
		if err == nil {
			n.fsm.Read(fn)
			return nil
		}
		if !errors.Is(err, raft.ErrNotLeader) && !errors.Is(err, raft.ErrLeadershipLost) {
			return err
		}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	id := n.cfg.ID + "-" + hex.EncodeToString(b[:])
	applied, cancel := n.fsm.Barrier(id)
	defer cancel()
	if err := n.Submit(ctx, state.Command{Op: state.OpReadBarrier, Key: id}); err != nil {
		return err
	}
	select {
	case <-applied:
	case <-ctx.Done():
		return ctx.Err()
	}
	n.fsm.Read(fn)
	return nil
}
