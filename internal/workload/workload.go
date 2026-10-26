// Package workload drives concurrent clients against the cluster's
// key/value register and records every operation in a history.
package workload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coolstack-coder/nebulachaos/internal/history"
)

type Config struct {
	Nodes   []string // node ids, e.g. n1
	APIs    []string // matching HTTP addresses
	Clients int
	Keys    int
	Timeout time.Duration // client-side request timeout
	Seed    uint64
	// Mix of operations, in percent. Reads get the rest.
	WritePct, CASPct int
}

type Workload struct {
	cfg   Config
	start time.Time

	mu      sync.Mutex
	ops     []history.Op
	nextID  int
	nextPID int
}

func New(cfg Config, start time.Time) *Workload {
	if cfg.Clients == 0 {
		cfg.Clients = 10
	}
	if cfg.Keys == 0 {
		cfg.Keys = 4
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.WritePct == 0 && cfg.CASPct == 0 {
		cfg.WritePct, cfg.CASPct = 30, 20
	}
	return &Workload{cfg: cfg, start: start, nextPID: cfg.Clients}
}

func (w *Workload) now() int64 { return time.Since(w.start).Nanoseconds() }

// History returns a copy of everything recorded so far.
func (w *Workload) History() []history.Op {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]history.Op(nil), w.ops...)
}

func (w *Workload) record(o history.Op) {
	w.mu.Lock()
	o.ID = w.nextID
	w.nextID++
	w.ops = append(w.ops, o)
	w.mu.Unlock()
}

func (w *Workload) freshProcess() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nextPID++
	return w.nextPID
}

// Run blocks until ctx is done.
func (w *Workload) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for c := 0; c < w.cfg.Clients; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.client(ctx, c)
		}()
	}
	wg.Wait()
}

func (w *Workload) client(ctx context.Context, id int) {
	r := rand.New(rand.NewPCG(w.cfg.Seed, uint64(id)))
	hc := &http.Client{Timeout: w.cfg.Timeout, Transport: &http.Transport{MaxIdleConnsPerHost: 2}}
	process := id
	node := id % len(w.cfg.APIs)
	seq := 0
	known := map[string]string{} // last value this client saw per key
	for ctx.Err() == nil {
		key := fmt.Sprintf("k%d", r.IntN(w.cfg.Keys))
		o := history.Op{Process: process, Client: id, Node: w.cfg.Nodes[node], Key: key}
		x := r.IntN(100)
		switch {
		case x < w.cfg.CASPct && known[key] != "":
			o.Kind, o.Expect = history.CAS, known[key]
		case x < w.cfg.CASPct+w.cfg.WritePct:
			o.Kind = history.Write
		default:
			o.Kind = history.Read
		}
		if o.Mutates() {
			seq++
			o.Value = fmt.Sprintf("%d.%d", process, seq)
		}
		w.do(ctx, hc, w.cfg.APIs[node], &o)
		w.record(o)
		if o.Status == history.OK {
			known[key] = o.Value
		}
		if o.Status == history.Info {
			// This process may still have a write in flight. Jepsen's rule:
			// retire it and continue as a new logical process.
			process = w.freshProcess()
		}
		if o.Status != history.OK {
			node = r.IntN(len(w.cfg.APIs)) // try somewhere else
		}
		time.Sleep(time.Duration(r.IntN(8)) * time.Millisecond)
	}
}

func (w *Workload) do(ctx context.Context, hc *http.Client, api string, o *history.Op) {
	var (
		method = http.MethodGet
		url    = "http://" + api + "/api/kv/" + o.Key
		body   []byte
	)
	switch o.Kind {
	case history.Write:
		method = http.MethodPut
		body, _ = json.Marshal(map[string]string{"value": o.Value})
	case history.CAS:
		method = http.MethodPost
		url += "/cas"
		body, _ = json.Marshal(map[string]string{"expect": o.Expect, "value": o.Value})
	}
	req, _ := http.NewRequestWithContext(context.WithoutCancel(ctx), method, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	o.Invoke = w.now()
	resp, err := hc.Do(req)
	o.Complete = w.now()
	if err != nil {
		o.Error = err.Error()
		// Connection refused: the request never reached a server, so it
		// definitely did not happen. Anything else (timeouts, resets) is
		// indeterminate.
		if errors.Is(err, syscall.ECONNREFUSED) {
			o.Status = history.Fail
		} else {
			o.Status = history.Info
		}
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	switch {
	case resp.StatusCode == http.StatusOK:
		o.Status = history.OK
		if o.Kind == history.Read {
			o.Value, o.Found = out.Value, out.Found
		}
	case strings.Contains(out.Error, "cas mismatch"):
		o.Status = history.Fail // the state machine ran the CAS and refused it
		o.Error = out.Error
	case resp.StatusCode == http.StatusBadRequest:
		o.Status = history.Fail
		o.Error = out.Error
	default:
		// 409/5xx for "timed out waiting for commit", "no leader" etc.:
		// the command may or may not have been applied.
		o.Status = history.Info
		o.Error = fmt.Sprintf("%d %s", resp.StatusCode, out.Error)
	}
}
