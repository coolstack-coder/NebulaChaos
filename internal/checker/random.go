package checker

import (
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/coolstack-coder/nebulachaos/internal/history"
)

// RandomHistory generates a single-key history that is linearizable by
// construction: every operation takes effect at a random instant inside its
// interval against a real register. With probability corrupt one observed
// value is then replaced, which usually (not always) breaks linearizability.
// It is how the two checkers are tested against each other.
func RandomHistory(r *rand.Rand, procs, opsPer int, corrupt float64) []history.Op {
	type pending struct {
		op history.Op
		at int64 // linearization point
		ok bool  // indeterminate ops: did it take effect
	}
	var all []pending
	id := 0
	for p := 0; p < procs; p++ {
		t := int64(r.IntN(20))
		for i := 0; i < opsPer; i++ {
			dur := int64(1 + r.IntN(40))
			o := history.Op{ID: id, Process: p, Client: p, Key: "k", Invoke: t, Complete: t + dur, Status: history.OK, Node: "n"}
			id++
			switch x := r.IntN(10); {
			case x < 4:
				o.Kind = history.Read
			case x < 7:
				o.Kind = history.Write
				o.Value = fmt.Sprintf("v%d", id)
			default:
				o.Kind = history.CAS
				o.Value = fmt.Sprintf("v%d", id)
			}
			pd := pending{op: o, at: t + r.Int64N(dur+1), ok: true}
			if o.Mutates() && r.IntN(8) == 0 {
				pd.op.Status = history.Info
				pd.ok = r.IntN(2) == 0
			}
			all = append(all, pd)
			t += dur + int64(r.IntN(10))
			if pd.op.Status == history.Info {
				break // this process is now stuck forever
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at < all[j].at })
	state := absent
	var written []string
	for i := range all {
		o := &all[i].op
		switch o.Kind {
		case history.Read:
			o.Value, o.Found = state, state != absent
		case history.Write:
			if all[i].ok {
				state = o.Value
				written = append(written, o.Value)
			}
		case history.CAS:
			// Usually expect the current value so CAS succeeds sometimes.
			if r.IntN(3) > 0 || len(written) == 0 {
				o.Expect = state
			} else {
				o.Expect = written[r.IntN(len(written))]
			}
			if state != absent && state == o.Expect {
				if all[i].ok {
					state = o.Value
					written = append(written, o.Value)
				}
			} else if o.Status == history.OK {
				o.Status, o.Error = history.Fail, "cas mismatch"
			}
		}
	}
	out := make([]history.Op, len(all))
	for i, p := range all {
		out[i] = p.op
	}
	if r.Float64() < corrupt && len(written) > 0 {
		for _, i := range r.Perm(len(out)) {
			if out[i].Kind == history.Read && out[i].Status == history.OK {
				out[i].Value, out[i].Found = written[r.IntN(len(written))], true
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Invoke < out[j].Invoke })
	return out
}
