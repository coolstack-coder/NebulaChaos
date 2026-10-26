package raft

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Regression tests for the recovery bugs NebulaChaos found: damage to data
// a node had already fsynced must stop the node, not be quietly dropped.

func writeLog(t *testing.T, dir string, n uint64) {
	t.Helper()
	s, err := NewFileStorage(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog(entries(1, n, 1)); err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestBitFlipInsideTheLogIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, 10)
	p := filepath.Join(dir, "raft.log")
	b, _ := os.ReadFile(p)
	b[len(b)/2] ^= 0x10 // a record in the middle, intact records after it
	os.WriteFile(p, b, 0o644)

	s, _ := NewFileStorage(dir, true)
	defer s.Close()
	if _, err := s.LoadLog(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("LoadLog = %v, want ErrCorrupt", err)
	}
	if st, _ := os.Stat(p); st.Size() != int64(len(b)) {
		t.Fatalf("recovery destroyed the evidence: log is %d bytes, was %d", st.Size(), len(b))
	}
}

func TestFlippedLengthFieldIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, 10)
	p := filepath.Join(dir, "raft.log")
	b, _ := os.ReadFile(p)
	b[3] = 0x7f // first record now claims to be ~2 GB long
	os.WriteFile(p, b, 0o644)

	s, _ := NewFileStorage(dir, true)
	defer s.Close()
	if _, err := s.LoadLog(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("LoadLog = %v, want ErrCorrupt", err)
	}
}

func TestTornTailIsStillRecovered(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, 10)
	p := filepath.Join(dir, "raft.log")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, b[:len(b)-3], 0o644) // the last record was half-written

	s, _ := NewFileStorage(dir, true)
	defer s.Close()
	got, err := s.LoadLog()
	if err != nil || len(got) != 9 {
		t.Fatalf("LoadLog = %d entries, %v; want 9, nil", len(got), err)
	}
}

func TestSnapshotHeaderIsChecksummed(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStorage(dir, true)
	if err := s.SaveSnapshot(1000, 3, []byte(`{"kv":{}}`)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	p := filepath.Join(dir, "snapshot.bin")
	b, _ := os.ReadFile(p)
	b[14] ^= 0x01 // a bit of the term
	os.WriteFile(p, b, 0o644)

	s, _ = NewFileStorage(dir, true)
	defer s.Close()
	if _, _, _, err := s.LoadSnapshot(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("LoadSnapshot = %v, want ErrCorrupt", err)
	}
}

func TestMissingSnapshotWithCompactedLogIsRefused(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStorage(dir, true)
	if err := s.SaveSnapshot(50, 1, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog(entries(51, 60, 1)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	os.Remove(filepath.Join(dir, "snapshot.bin")) // a lost rename after a crash

	s, _ = NewFileStorage(dir, true)
	_, err := New(Config{ID: "n1", Peers: map[string]string{"n1": "n1"}, Storage: s, Transport: nil, FSM: nopFSM{}})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("New = %v, want ErrCorrupt (upstream started with an empty log)", err)
	}
}

type nopFSM struct{}

func (nopFSM) Apply(uint64, []byte) any  { return nil }
func (nopFSM) Snapshot() ([]byte, error) { return nil, nil }
func (nopFSM) Restore([]byte) error      { return nil }
