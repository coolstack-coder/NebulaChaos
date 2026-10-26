// Package checker decides whether a recorded history could have been
// produced by a single, correct register: is it linearizable?
//
// It ships two independent implementations of the same question. wgl.go is
// a from-scratch Wing–Gong–Lowe search; porcupine.go adapts the same model to
// github.com/anishathalye/porcupine. The test suite runs both on thousands
// of random histories and requires them to agree, so a bug in either one
// shows up as a disagreement rather than as a silently wrong verdict.
package checker

import (
	"github.com/coolstack-coder/nebulachaos/internal/history"
)

// absent is the register state before any write. Workload values are never
// empty, so the empty string is free to mean "no value".
const absent = ""

// prepare drops operations that carry no information and normalises the
// rest:
//   - a read that did not return (fail or info) says nothing;
//   - a write that definitely failed never happened;
//   - a CAS that definitely failed is kept: it says the value was not the
//     expected one at some point during the call;
//   - an indeterminate (info) write or CAS may take effect at any time after
//     it was invoked, so it is left open-ended.
func prepare(ops []history.Op) []history.Op {
	out := make([]history.Op, 0, len(ops))
	for _, o := range ops {
		switch {
		case o.Kind == history.Read && o.Status != history.OK:
		case o.Kind == history.Write && o.Status == history.Fail:
		default:
			out = append(out, o)
		}
	}
	return out
}

// step applies op to state and reports whether the op's observed result is
// possible from that state.
func step(state string, o history.Op) (bool, string) {
	switch o.Kind {
	case history.Read:
		if !o.Found {
			return state == absent, state
		}
		return o.Value == state, state
	case history.Write:
		return true, o.Value
	case history.CAS:
		match := state != absent && state == o.Expect
		switch o.Status {
		case history.OK:
			if match {
				return true, o.Value
			}
			return false, state
		case history.Fail:
			return !match, state
		default: // info: it either happened here or (by being ordered last) never
			if match {
				return true, o.Value
			}
			return true, state
		}
	}
	return false, state
}
