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
