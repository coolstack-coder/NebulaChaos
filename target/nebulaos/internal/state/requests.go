package state

import (
	"errors"
	"strings"
)

// maxRequests bounds the dedup table. A retry arrives within seconds, so
// remembering the last few thousand requests is plenty.
const maxRequests = 4096

// RequestTable remembers the outcome of recently applied requests. It is
// part of the replicated state (and of snapshots), so every replica makes
// the same decision about a duplicate. Order is insertion order; the oldest
// entries are evicted first.
type RequestTable struct {
	Order   []string          `json:"order"`
	Results map[string]string `json:"results"` // "" = success, else the error text
}

// Lookup returns the original outcome if id was already applied.
func (t *RequestTable) Lookup(id string) (error, bool) {
	if t == nil || id == "" {
		return nil, false
	}
	msg, ok := t.Results[id]
	if !ok {
		return nil, false
	}
	if msg == "" {
		return nil, true
	}
	if rest, cas := strings.CutPrefix(msg, ErrCASMismatch.Error()); cas {
		return casErr(rest), true
	}
	return errors.New(msg), true
}

// Record stores the outcome of a newly applied request.
func (t *RequestTable) Record(id string, err error) *RequestTable {
	if id == "" {
		return t
	}
	if t == nil {
		t = &RequestTable{Results: map[string]string{}}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	t.Results[id] = msg
	t.Order = append(t.Order, id)
	for len(t.Order) > maxRequests {
		delete(t.Results, t.Order[0])
		t.Order = t.Order[1:]
	}
	return t
}

func (t *RequestTable) clone() *RequestTable {
	c := &RequestTable{Order: append([]string(nil), t.Order...), Results: make(map[string]string, len(t.Results))}
	for k, v := range t.Results {
		c.Results[k] = v
	}
	return c
}

type casMismatch struct{ rest string }

func (e casMismatch) Error() string { return ErrCASMismatch.Error() + e.rest }
func (e casMismatch) Unwrap() error { return ErrCASMismatch }

func casErr(rest string) error { return casMismatch{rest} }
