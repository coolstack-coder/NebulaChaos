package checker

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coolstack-coder/nebulachaos/internal/history"
)

// KeyReport is the verdict for one key.
type KeyReport struct {
	Key     string       `json:"key"`
	Ops     int          `json:"ops"`
	Result  string       `json:"result"`
	Minimal []history.Op `json:"minimal,omitempty"` // smallest failing sub-history
	Anomaly string       `json:"anomaly,omitempty"`
	Explain []string     `json:"explain,omitempty"`
}

// Report is the verdict for a whole history.
type Report struct {
	Valid      bool          `json:"valid"`
	Unknown    bool          `json:"unknown"`
	Ops        int           `json:"ops"`
	OK         int           `json:"ok"`
	Fail       int           `json:"fail"`
	Info       int           `json:"info"`
	Keys       []KeyReport   `json:"keys"`
	Porcupine  string        `json:"porcupine"`
	Agree      bool          `json:"checkers_agree"`
	CheckTime  time.Duration `json:"check_time_ns"`
	Violations int           `json:"violations"`
}

// Check verifies a history with both checkers, shrinks every failing key to
// a minimal counterexample and explains it.
func Check(ops []history.Op, budget int) Report {
	start := time.Now()
	r := Report{Ops: len(ops), Valid: true}
	for _, o := range ops {
		switch o.Status {
		case history.OK:
			r.OK++
		case history.Fail:
			r.Fail++
		default:
			r.Info++
		}
	}
	by := history.ByKey(ops)
	keys := history.Keys(by)
	r.Keys = make([]KeyReport, len(keys))
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			kr := KeyReport{Key: k, Ops: len(by[k])}
			res, min := CheckKey(by[k], budget)
			kr.Result = res.String()
			if res == Violation {
				kr.Minimal = min
				if kr.Minimal == nil {
					kr.Minimal = Shrink(by[k], budget)
				}
				sort.Slice(kr.Minimal, func(i, j int) bool { return kr.Minimal[i].Invoke < kr.Minimal[j].Invoke })
				kr.Anomaly, kr.Explain = Explain(kr.Minimal, by[k])
			}
			r.Keys[i] = kr
		}()
	}
	wg.Wait()
	ours := Linearizable
	for _, kr := range r.Keys {
		switch kr.Result {
		case Violation.String():
			r.Valid = false
			r.Violations++
			ours = Violation
		case Unknown.String():
			r.Unknown = true
			if ours == Linearizable {
				ours = Unknown
			}
		}
	}
	pres, _ := CheckPorcupine(ops, 20*time.Second)
	r.Porcupine = pres.String()
	r.Agree = pres == ours || pres == Unknown || ours == Unknown
	r.CheckTime = time.Since(start)
	return r
}

// Shrink reduces a non-linearizable single-key history to a 1-minimal one
// by delta debugging: removing any single remaining operation makes it
// linearizable, or would remove the only write of a value that a remaining
// read observed. That second rule keeps the counterexample explainable;
// without it "read x=a" alone is the minimal (and useless) answer.
func Shrink(ops []history.Op, budget int) []history.Op {
	cur := prepare(ops)
	if w := findWitness(cur); w != nil {
		return shrinkPrepared(w, cur, budget)
	}
	if fast := dropUnobserved(cur); len(fast) < len(cur) {
		if res, _ := search(fast, budget); res == Violation {
			if m := shrinkPrepared(fast, fast, budget); selfEvident(m, fast) {
				return m
			}
		}
	}
	return shrinkPrepared(cur, cur, budget)
}

// shrinkPrepared minimises cur, keeping it closed with respect to full.
func shrinkPrepared(cur, full []history.Op, budget int) []history.Op {
	bad := func(c []history.Op) bool {
		if !closed(c, full) {
			return false
		}
		res, _ := search(c, budget)
		return res == Violation
	}
	if !bad(cur) {
		return cur
	}
	n := 2
	for len(cur) >= 2 {
		chunk := (len(cur) + n - 1) / n
		reduced := false
		for start := 0; start < len(cur); start += chunk {
			end := min(start+chunk, len(cur))
			cand := append(append([]history.Op{}, cur[:start]...), cur[end:]...)
			if len(cand) > 0 && bad(cand) {
				cur = cand
				n = max(n-1, 2)
				reduced = true
				break
			}
		}
		if !reduced {
			if n >= len(cur) {
				break
			}
			n = min(2*n, len(cur))
		}
	}
	sort.Slice(cur, func(i, j int) bool { return cur[i].Invoke < cur[j].Invoke })
	return cur
}

// observed returns the value an op saw, if any: a read's result or the
// expected value of a successful CAS.
func observed(o history.Op) (string, bool) {
	switch {
	case o.Kind == history.Read && o.Status == history.OK && o.Found:
		return o.Value, true
	case o.Kind == history.CAS && o.Status == history.OK:
		return o.Expect, true
	}
	return "", false
}

// closed reports whether every value observed in c that some op in full
// wrote is also written by an op in c.
func closed(c, full []history.Op) bool {
	has := func(ops []history.Op, v string) bool {
		for _, o := range ops {
			if o.Mutates() && o.Value == v && o.Status != history.Fail {
				return true
			}
		}
		return false
	}
	for _, o := range c {
		if v, ok := observed(o); ok && !has(c, v) && has(full, v) {
			return false
		}
	}
	return true
}

func secs(ns int64) string { return fmt.Sprintf("%.3fs", float64(ns)/1e9) }

func span(o history.Op) string {
	if o.Status == history.Info {
		return fmt.Sprintf("[%s, never returned]", secs(o.Invoke))
	}
	return fmt.Sprintf("[%s, %s]", secs(o.Invoke), secs(o.Complete))
}

// Explain names the anomaly in a minimal counterexample and describes it in
// plain words, the way you would explain it to a colleague.
// all is the full history of the key, used to find writes the shrinker
// dropped because the client was told they failed.
func Explain(min, all []history.Op) (string, []string) {
	var lines []string
	for _, o := range min {
		lines = append(lines, fmt.Sprintf("p%d via %s  %-34s %s", o.Process, o.Node, o.String(), span(o)))
	}
	writers := func(v string) []history.Op {
		var w []history.Op
		for _, o := range min {
			if o.Mutates() && o.Value == v && o.Status != history.Fail {
				w = append(w, o)
			}
		}
		return w
	}
	// A value observed by a read (or a successful CAS) is a "witness".
	for _, r := range min {
		var seen string
		var found bool
		switch {
		case r.Kind == history.Read && r.Status == history.OK:
			seen, found = r.Value, r.Found
		case r.Kind == history.CAS && r.Status == history.OK:
			seen, found = r.Expect, true
		default:
			continue
		}
		ws := writers(seen)
		if found && len(ws) == 0 {
			for _, x := range all {
				if x.Mutates() && x.Value == seen && x.Status == history.Fail {
					lines = append([]string{fmt.Sprintf("p%d via %s  %-34s %s  error: %s", x.Process, x.Node, x.String(), span(x), x.Error)}, lines...)
					return "write reported as failed took effect", append(lines, fmt.Sprintf(
						"The client was told %s failed (%q), yet %s later read its value %q. The cluster applied the command and then answered as if it had not: the client got the opposite of what happened.",
						x.String(), x.Error, describe(r), seen))
				}
			}
			return "phantom read", append(lines, fmt.Sprintf(
				"%s observed %q, a value no operation in the history ever wrote.", r.String(), seen))
		}
		// Was the observed value overwritten, before r began, by a write
		// that itself started after every write of the observed value
		// finished?
		for _, w := range min {
			if !w.Mutates() || w.Status != history.OK || w.Value == seen || w.Complete >= r.Invoke {
				continue
			}
			after := true
			for _, x := range ws {
				if x.Status == history.Info || x.Complete >= w.Invoke {
					after = false
				}
			}
			if !found {
				after = true // nothing written before is consistent with ∅ once w completed
			}
			if after {
				what := fmt.Sprintf("%q", seen)
				if !found {
					what = "nothing (∅)"
				}
				return "stale read", append(lines, fmt.Sprintf(
					"%s saw %s, but %s had already completed at %s, before the read began at %s. A single correct register can never go back to an older value.",
					describe(r), what, w.String(), secs(w.Complete), secs(r.Invoke)))
			}
		}
	}
	// Two observations, one strictly after the other, of different values,
	// when every write of either value had finished before the first one
	// began and nothing else wrote in between: the register held a single
	// value the whole time, so one of the two observers was served stale
	// data.
	for _, a := range min {
		va, ok := observed(a)
		if !ok {
			continue
		}
		for _, b := range min {
			vb, ok := observed(b)
			if !ok || va == vb || a.Complete >= b.Invoke {
				continue
			}
			settled := true
			for _, x := range append(writers(va), writers(vb)...) {
				if x.Status == history.Info || x.Complete >= a.Invoke {
					settled = false
				}
			}
			for _, x := range min {
				if x.Mutates() && x.Value != va && x.Value != vb && x.ID != b.ID &&
					x.Invoke < b.Complete && (x.Status == history.Info || x.Complete > a.Invoke) {
					settled = false
				}
			}
			if settled && len(writers(va)) > 0 && len(writers(vb)) > 0 {
				return "stale read", append(lines, fmt.Sprintf(
					"Both writes had finished before %s started, so the register held one final value. Yet %s saw %q and then %s, which began later, saw %q. One of the two was served a stale value.",
					a.String(), describe(a), va, describe(b), vb))
			}
		}
	}
	// A value was observed, and a compare-and-set from exactly that value,
	// issued afterwards with nothing else written in between, was refused:
	// the observer was shown a value the register did not hold.
	for _, a := range min {
		va, ok := observed(a)
		if !ok {
			continue
		}
		for _, b := range min {
			if b.Kind != history.CAS || b.Status != history.Fail || b.Expect != va || a.Complete >= b.Invoke {
				continue
			}
			quiet := true
			for _, x := range min {
				if x.Mutates() && x.Value != va && x.ID != b.ID && x.Invoke < b.Complete &&
					(x.Status == history.Info || x.Complete > a.Invoke) {
					quiet = false
				}
			}
			if quiet {
				return "inconsistent read", append(lines, fmt.Sprintf(
					"%s saw %q, but %s, issued after it with no other write in between, was refused because the value was not %q. The read was shown a value the cluster did not actually hold.",
					describe(a), va, b.String(), va))
			}
		}
	}
	// Two successful CAS from the same expected value: both cannot have
	// seen it.
	for i, a := range min {
		for _, b := range min[i+1:] {
			if a.Kind == history.CAS && b.Kind == history.CAS && a.Status == history.OK && b.Status == history.OK &&
				a.Expect == b.Expect && len(writers(a.Expect)) == 1 {
				return "double compare-and-set", append(lines, fmt.Sprintf(
					"Both %s and %s succeeded, but %q was written only once: one of them must have seen a value that was already gone.",
					a.String(), b.String(), a.Expect))
			}
		}
	}
	return "non-linearizable", append(lines,
		"No order of these operations, consistent with when each one started and finished, is legal for a single register.")
}

func describe(o history.Op) string {
	s := o.String()
	if o.Kind == history.CAS {
		s = strings.Replace(s, "cas(", "the successful cas(", 1)
	}
	return fmt.Sprintf("%s on %s", s, o.Node)
}
