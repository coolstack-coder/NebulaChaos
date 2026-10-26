package state

import (
	"errors"
	"fmt"
)

// ErrCASMismatch is returned when a compare-and-set finds a different value.
// It is a definite failure: the command was applied and changed nothing.
var ErrCASMismatch = errors.New("cas mismatch")

func (s *Snapshot) applyKV(c *Command) error {
	if c.Key == "" || c.Value == nil {
		return errors.New("kv: key and value are required")
	}
	if c.Op == OpKVCAS {
		cur, ok := s.KV[c.Key]
		if c.Expect == nil || !ok || cur != *c.Expect {
			return fmt.Errorf("%w: key %q", ErrCASMismatch, c.Key)
		}
	}
	s.KV[c.Key] = *c.Value
	return nil
}

// Barrier returns a channel that is closed once this replica has applied the
// read barrier with the given id. A replica that has applied a barrier has
// also applied every write committed before it, which is what makes a read
// on a follower linearizable.
func (f *FSM) Barrier(id string) (<-chan struct{}, func()) {
	ch := make(chan struct{})
	f.bmu.Lock()
	if f.barriers == nil {
		f.barriers = map[string]chan struct{}{}
	}
	f.barriers[id] = ch
	f.bmu.Unlock()
	return ch, func() {
		f.bmu.Lock()
		delete(f.barriers, id)
		f.bmu.Unlock()
	}
}

func (f *FSM) releaseBarrier(id string) {
	f.bmu.Lock()
	defer f.bmu.Unlock()
	if ch, ok := f.barriers[id]; ok {
		close(ch)
		delete(f.barriers, id) // a retried barrier may be applied twice
	}
}
