package state

import (
	"errors"
	"testing"
)

// The bug NebulaChaos found: Submit retries a command after a leader change,
// the first attempt had already committed, so the command ran twice. A CAS
// that succeeded was then reported to the client as a mismatch.
func TestRetriedCommandIsAppliedOnce(t *testing.T) {
	f := NewFSM()
	v1, v2 := "a", "b"
	f.Apply(1, Command{Op: OpKVPut, Key: "x", Value: &v1, ReqID: "w1"}.Encode())
	cas := Command{Op: OpKVCAS, Key: "x", Expect: &v1, Value: &v2, ReqID: "c1"}.Encode()
	if err, _ := f.Apply(2, cas).(error); err != nil {
		t.Fatalf("first CAS: %v", err)
	}
	if err, _ := f.Apply(3, cas).(error); err != nil {
		t.Fatalf("retried CAS must report the original success, got %v", err)
	}
	other := Command{Op: OpKVCAS, Key: "x", Expect: &v1, Value: &v2, ReqID: "c2"}.Encode()
	if err, _ := f.Apply(4, other).(error); !errors.Is(err, ErrCASMismatch) {
		t.Fatalf("a different request must still be evaluated, got %v", err)
	}
	if err, _ := f.Apply(5, other).(error); !errors.Is(err, ErrCASMismatch) {
		t.Fatalf("its retry must repeat the original mismatch, got %v", err)
	}
	snap, _ := f.Snapshot()
	g := NewFSM()
	if err := g.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if err, _ := g.Apply(6, cas).(error); err != nil {
		t.Fatalf("dedup must survive a snapshot: %v", err)
	}
}
