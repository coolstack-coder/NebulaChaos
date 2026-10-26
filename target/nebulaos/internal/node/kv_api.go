package node

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/coolstack-coder/nebulaos/internal/state"
)

// A tiny replicated key/value register. It exists so an external tester can
// check the cluster for linearizability: writes go through exactly the same
// Submit path as every other command, reads through the same local replica
// as GET /api/services.
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
	n.fsm.Read(func(s *state.Snapshot) { v, ok = s.KV[r.PathValue("key")] })
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
