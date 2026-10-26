package checker

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/coolstack-coder/nebulachaos/internal/history"
)

func op(p int, kind history.Kind, val string, inv, comp int64) history.Op {
	return history.Op{Process: p, Kind: kind, Key: "x", Value: val, Found: val != "", Invoke: inv, Complete: comp, Status: history.OK}
}

func TestStaleReadIsCaught(t *testing.T) {
	h := []history.Op{
		op(1, history.Write, "a", 0, 10),
		op(2, history.Write, "b", 20, 30),
		op(3, history.Read, "a", 40, 50), // b completed before this began
	}
	res, _ := CheckKey(h, 0)
	if res != Violation {
		t.Fatalf("got %v, want violation", res)
	}
	min := Shrink(h, 0)
	if len(min) != 3 {
		t.Fatalf("minimal history has %d ops, want 3", len(min))
	}
	if kind, _ := Explain(min, h); kind != "stale read" {
		t.Fatalf("explained as %q", kind)
	}
}

func TestConcurrentReadMaySeeEither(t *testing.T) {
	h := []history.Op{
		op(1, history.Write, "a", 0, 10),
		op(2, history.Write, "b", 20, 60),
		op(3, history.Read, "a", 30, 40), // overlaps the write of b
		op(4, history.Read, "b", 45, 50),
	}
	if res, _ := CheckKey(h, 0); res != Linearizable {
		t.Fatalf("got %v, want linearizable", res)
	}
}

func TestIndeterminateWriteMayLandLate(t *testing.T) {
	w := op(1, history.Write, "b", 0, 0)
	w.Status = history.Info
	h := []history.Op{
		op(2, history.Write, "a", 5, 10),
		w,
		op(3, history.Read, "a", 20, 30),
		op(4, history.Read, "b", 100, 110), // the timed-out write landed later
	}
	if res, _ := CheckKey(h, 0); res != Linearizable {
		t.Fatalf("got %v, want linearizable", res)
	}
	// Treating the same timeout as a failure would wrongly flag it.
	h[1].Status = history.Fail
	if res, _ := CheckKey(h, 0); res != Violation {
		t.Fatalf("got %v, want violation when the write is assumed failed", res)
	}
}

// The two checkers must agree on every history.
func TestCheckersAgreeOnRandomHistories(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	n := 4000
	if testing.Short() {
		n = 400
	}
	var bad int
	for i := 0; i < n; i++ {
		h := RandomHistory(r, 2+r.IntN(5), 1+r.IntN(8), 0.5)
		ours, _ := CheckKey(h, 0)
		theirs, _ := CheckPorcupine(h, 10*time.Second)
		if ours != theirs {
			t.Fatalf("history %d: wgl=%v porcupine=%v\n%v", i, ours, theirs, h)
		}
		if ours == Violation {
			bad++
			min := Shrink(h, 0)
			if res, _ := CheckKey(min, 0); res != Violation {
				t.Fatalf("shrunk history is linearizable")
			}
			for j := range min {
				rest := append(append([]history.Op{}, min[:j]...), min[j+1:]...)
				if res, _ := CheckKey(rest, 0); res == Violation && closed(rest, prepare(h)) {
					t.Fatalf("shrunk history is not 1-minimal")
				}
			}
		}
	}
	t.Logf("%d histories, %d non-linearizable, checkers agreed on all", n, bad)
	if bad == 0 || bad == n {
		t.Fatalf("generator is degenerate: %d/%d violations", bad, n)
	}
}

func TestGeneratedHistoriesAreLinearizableWithoutCorruption(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
	for i := 0; i < 2000; i++ {
		h := RandomHistory(r, 2+r.IntN(5), 1+r.IntN(8), 0)
		if res, _ := CheckKey(h, 0); res != Linearizable {
			t.Fatalf("history %d should be linearizable by construction:\n%v", i, h)
		}
	}
}

// A CAS that was refused at the TCP level never ran; it must not be read
// as "the value was not the expected one" (this was a false positive in an
// early version of NebulaChaos).
func TestRefusedCASCarriesNoInformation(t *testing.T) {
	cas := history.Op{Process: 2, Kind: history.CAS, Key: "x", Expect: "a", Value: "b",
		Invoke: 20, Complete: 20, Status: history.Fail, Error: "connect: connection refused"}
	h := []history.Op{op(1, history.Write, "a", 0, 10), cas}
	if res, _ := CheckKey(h, 0); res != Linearizable {
		t.Fatalf("got %v, want linearizable", res)
	}
	h[1].Error = "cas mismatch: key x"
	if res, _ := CheckKey(h, 0); res != Violation {
		t.Fatalf("an evaluated mismatch after the write completed must be a violation, got %v", res)
	}
}
