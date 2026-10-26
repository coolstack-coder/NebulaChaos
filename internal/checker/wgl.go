package checker

import (
	"math"
	"sort"

	"github.com/coolstack-coder/nebulachaos/internal/history"
)

// Result of checking one key.
type Result int

const (
	Linearizable Result = iota
	Violation
	Unknown // search budget exhausted
)

func (r Result) String() string {
	return [...]string{"linearizable", "NOT linearizable", "unknown"}[r]
}

// entry is a call or return event in the doubly linked event list that the
// Wing–Gong algorithm (with Lowe's memoisation) walks.
type entry struct {
	op         int
	call       bool
	t          int64
	match      *entry // call → its return
	prev, next *entry
}

type bitset []uint64

func newBitset(n int) bitset       { return make(bitset, (n+63)/64) }
func (b bitset) set(i int)         { b[i/64] |= 1 << (i % 64) }
func (b bitset) clear(i int)       { b[i/64] &^= 1 << (i % 64) }
func (b bitset) key(s string) string {
	buf := make([]byte, 0, len(b)*8+len(s)+1)
	for _, w := range b {
		for i := 0; i < 8; i++ {
			buf = append(buf, byte(w>>(8*i)))
		}
	}
	buf = append(buf, 0)
	return string(append(buf, s...))
}

// CheckKey searches for a linearization of a single-key history. On failure
// it also returns the longest prefix of operations it managed to linearize,
// which is where the explanation starts. budget bounds the number of search
// steps (0 = unlimited).
func CheckKey(raw []history.Op, budget int) (Result, []history.Op) {
	ops := prepare(raw)
	if len(ops) == 0 {
		return Linearizable, nil
	}
	// Build the event list. Intervals are closed, so on equal timestamps a
	// call sorts before a return (the two operations overlap).
	events := make([]*entry, 0, 2*len(ops))
	for i, o := range ops {
		ret := o.Complete
		if o.Status == history.Info {
			ret = math.MaxInt64
		}
		c := &entry{op: i, call: true, t: o.Invoke}
		r := &entry{op: i, t: ret}
		c.match = r
		events = append(events, c, r)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].t != events[j].t {
			return events[i].t < events[j].t
		}
		return events[i].call && !events[j].call
	})
	head := &entry{}
	prev := head
	for _, e := range events {
		prev.next, e.prev = e, prev
		prev = e
	}

	lift := func(e *entry) {
		e.prev.next = e.next
		e.next.prev = e.prev
		m := e.match
		m.prev.next = m.next
		if m.next != nil {
			m.next.prev = m.prev
		}
	}
	unlift := func(e *entry) {
		m := e.match
		m.prev.next = m
		if m.next != nil {
			m.next.prev = m
		}
		e.prev.next = e
		e.next.prev = e
	}

	type frame struct {
		e     *entry
		state string
	}
	var (
		stack []frame
		state = absent
		lin   = newBitset(len(ops))
		seen  = map[string]struct{}{}
		best  []int
		steps int
	)
	e := head.next
	for head.next != nil {
		if budget > 0 {
			if steps++; steps > budget {
				return Unknown, nil
			}
		}
		if e.call {
			if ok, next := step(state, ops[e.op]); ok {
				lin.set(e.op)
				k := lin.key(next)
				if _, dup := seen[k]; !dup {
					seen[k] = struct{}{}
					stack = append(stack, frame{e, state})
					state = next
					lift(e)
					if len(stack) > len(best) {
						best = best[:0]
						for _, f := range stack {
							best = append(best, f.e.op)
						}
					}
					e = head.next
					continue
				}
				lin.clear(e.op)
			}
			e = e.next
			continue
		}
		// We reached the return of an operation we have not linearized:
		// it would have to take effect after it already returned. Undo.
		if len(stack) == 0 {
			out := make([]history.Op, len(best))
			for i, idx := range best {
				out[i] = ops[idx]
			}
			return Violation, out
		}
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		state = f.state
		lin.clear(f.e.op)
		unlift(f.e)
		e = f.e.next
	}
	return Linearizable, nil
}
