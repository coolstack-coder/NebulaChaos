// Package history defines the record of everything clients did during a
// test run: one entry per operation with when it was invoked, when it
// completed, and what the cluster answered.
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Kind of operation against the key/value register.
type Kind string

const (
	Read  Kind = "read"
	Write Kind = "write"
	CAS   Kind = "cas"
)

// Status is the outcome as the client saw it.
type Status string

const (
	// OK: the operation definitely took effect (or, for a read, returned
	// this value).
	OK Status = "ok"
	// Fail: the operation definitely did not take effect.
	Fail Status = "fail"
	// Info: the client cannot know. A timed-out write may still commit
	// later, so the checker must allow both outcomes. Treating these as
	// failures is the most common way to make a broken system look correct.
	Info Status = "info"
)

// Op is one completed (or abandoned) client operation. Times are
// nanoseconds since the start of the run.
type Op struct {
	ID       int    `json:"id"`
	Process  int    `json:"process"` // logical client; a new one replaces any client whose op went "info"
	Client   int    `json:"client"`  // physical client goroutine
	Node     string `json:"node"`    // node the request was sent to
	Kind     Kind   `json:"kind"`
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`  // written value, or the value a read returned
	Expect   string `json:"expect,omitempty"` // CAS: expected current value
	Found    bool   `json:"found,omitempty"`  // read: key existed
	Invoke   int64  `json:"invoke"`
	Complete int64  `json:"complete"`
	Status   Status `json:"status"`
	Error    string `json:"error,omitempty"`
}

func (o Op) String() string {
	var s string
	switch o.Kind {
	case Read:
		v := o.Value
		if !o.Found {
			v = "∅"
		}
		s = fmt.Sprintf("read(%s) → %s", o.Key, v)
		if o.Status != OK {
			s = fmt.Sprintf("read(%s)", o.Key)
		}
	case Write:
		s = fmt.Sprintf("write(%s, %s)", o.Key, o.Value)
	case CAS:
		s = fmt.Sprintf("cas(%s, %s → %s)", o.Key, orNil(o.Expect), o.Value)
	}
	return fmt.Sprintf("%s [%s]", s, o.Status)
}

func orNil(s string) string {
	if s == "" {
		return "∅"
	}
	return s
}

// Mutates reports whether the op can change state.
func (o Op) Mutates() bool { return o.Kind != Read }

// Save writes ops as JSON lines.
func Save(path string, ops []Op) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, o := range ops {
		if err := enc.Encode(o); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Load reads a JSON-lines history.
func Load(path string) ([]Op, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Op
	dec := json.NewDecoder(bufio.NewReader(f))
	for dec.More() {
		var o Op
		if err := dec.Decode(&o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// ByKey splits a history into per-key sub-histories. Linearizability is
// compositional (Herlihy & Wing), so each key can be checked on its own.
func ByKey(ops []Op) map[string][]Op {
	out := map[string][]Op{}
	for _, o := range ops {
		out[o.Key] = append(out[o.Key], o)
	}
	return out
}

// Keys returns the keys of a ByKey map in sorted order.
func Keys(m map[string][]Op) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
