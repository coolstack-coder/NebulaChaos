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

func newBitset(n int) bitset { return make(bitset, (n+63)/64) }
func (b bitset) set(i int)   { b[i/64] |= 1 << (i % 64) }
func (b bitset) clear(i int) { b[i/64] &^= 1 << (i % 64) }
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
//
// Indeterminate writes are what make the search expensive: each one may
// take effect anywhere after its call. An indeterminate write whose value
// nobody ever observed can always be linearized as "never happened", so the
// search first runs without those:
//   - if that history is linearizable, so is the full one;
//   - if it is not, it is shrunk to a minimal counterexample. When that
//     counterexample is self-evident (see selfEvident) it is a sub-history
//     of the full history that no linearization of the full history could
//     satisfy, so the full history is not linearizable either;
//   - otherwise the full history is searched, within the budget.
func CheckKey(raw []history.Op, budget int) (Result, []history.Op) {
	ops := prepare(raw)
	if w := findWitness(ops); w != nil {
		return Violation, shrinkPrepared(w, ops, budget)
	}
	if fast := dropUnobserved(ops); len(fast) < len(ops) {
		switch res, _ := search(fast, budget); res {
		case Linearizable:
			return Linearizable, nil
		case Violation:
			if m := shrinkPrepared(fast, fast, budget); selfEvident(m, fast) {
				return Violation, m
			}
		}
	}
	res, _ := search(ops, budget)
	return res, nil
}

// findWitness looks directly for the two anomalies that dominate real
// failures, in O(n log n), before any search:
//   - a value observed that no operation (other than ones that definitely
//     failed) ever wrote;
//   - a stale observation: the value v was written by w_v, a different
//     write w began after w_v had finished and itself finished before the
//     observer began. Real time forces w_v < w < observer, and v is written
//     only once, so the observer cannot see v.
//
// Either is a self-evident counterexample (see selfEvident).
func findWitness(ops []history.Op) []history.Op {
	writer := map[string]history.Op{}
	var done []history.Op // mutations that definitely took effect
	for _, o := range ops {
		if o.Mutates() && o.Status != history.Fail {
			writer[o.Value] = o
		}
		if o.Mutates() && o.Status == history.OK {
			done = append(done, o)
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].Complete < done[j].Complete })
	// latest[i] is, among done[:i+1], the one invoked last.
	latest := make([]int, len(done))
	for i := range done {
		latest[i] = i
		if i > 0 && done[latest[i-1]].Invoke > done[i].Invoke {
			latest[i] = latest[i-1]
		}
	}
	for _, r := range ops {
		var (
			v     string
			found bool
		)
		switch {
		case r.Kind == history.Read && r.Status == history.OK:
			v, found = r.Value, r.Found
		case r.Kind == history.CAS && r.Status == history.OK:
			v, found = r.Expect, true
		default:
			continue
		}
		wv, written := writer[v]
		if found && !written {
			return []history.Op{r}
		}
		bound := int64(math.MinInt64) // ∅ is stale once anything completed
		if found {
			if wv.Status != history.OK {
				continue // may have landed arbitrarily late
			}
			bound = wv.Complete
		}
		n := sort.Search(len(done), func(i int) bool { return done[i].Complete >= r.Invoke })
		if n == 0 {
			continue
		}
		if w := done[latest[n-1]]; w.Invoke > bound {
			out := []history.Op{w, r}
			if found {
				out = append(out, wv)
			}
			// A CAS used as the overwrite observed a value too; keep its
			// writer so the counterexample stays closed and explainable.
			if we, ok := writer[w.Expect]; w.Kind == history.CAS && ok {
				out = append(out, we)
			}
			return out
		}
	}
	return nil
}

// selfEvident reports whether a non-linearizable sub-history stays
// non-linearizable inside any larger history. With every value written by
// exactly one operation (the workload guarantees it) that holds when the
// sub-history keeps the writer of every value it observes and has no CAS
// that failed or timed out: removing operations from a linearization can
// then only remove state changes, never add one between a write and the
// read that observed it.
func selfEvident(m, full []history.Op) bool {
	if len(m) == 0 {
		return false
	}
	for _, o := range m {
		if o.Kind == history.CAS && o.Status != history.OK {
			return false
		}
	}
	return closed(m, full)
}

func dropUnobserved(ops []history.Op) []history.Op {
	seen := map[string]bool{}
	for _, o := range ops {
		if v, ok := observed(o); ok {
			seen[v] = true
		}
		if o.Kind == history.CAS {
			seen[o.Expect] = true
		}
	}
	out := make([]history.Op, 0, len(ops))
	for _, o := range ops {
		if o.Status == history.Info && o.Mutates() && !seen[o.Value] {
			continue
		}
		out = append(out, o)
	}
	return out
}

// search is the WGL search over an already prepared history.
func search(ops []history.Op, budget int) (Result, []history.Op) {
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
