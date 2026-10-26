package raft

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sync"

	pb "github.com/coolstack-coder/nebulaos/gen/nebula/v1"
	"google.golang.org/protobuf/proto"
)

// Storage persists the three pieces of Raft state that must survive a crash:
// the hard state (term + vote), the log, and the latest snapshot.
//
// The log is an append-only file of length-prefixed, CRC-checked protobuf
// records. Appends are the hot path and only ever fsync the tail; rewrites
// (conflict truncation, compaction) go through a temp file + rename so a
// crash mid-rewrite leaves the previous file intact.
type Storage interface {
	LoadState() (term uint64, votedFor string, err error)
	SaveState(term uint64, votedFor string) error
	LoadLog() ([]*pb.LogEntry, error)
	AppendLog(entries []*pb.LogEntry) error
	// AppendLogNoSync writes entries without waiting for the disk; a later
	// Sync makes them durable. The leader uses this for group commit.
	AppendLogNoSync(entries []*pb.LogEntry) error
	Sync() error
	RewriteLog(entries []*pb.LogEntry) error
	LoadSnapshot() (index, term uint64, data []byte, err error)
	SaveSnapshot(index, term uint64, data []byte) error
	Close() error
}

type hardState struct {
	Term     uint64 `json:"term"`
	VotedFor string `json:"voted_for"`
}

// FileStorage is the on-disk Storage implementation.
type FileStorage struct {
	dir   string
	sync  bool
	crcTb *crc32.Table

	mu   sync.Mutex // guards logF, which RewriteLog swaps
	logF *os.File
	// gen counts log rewrites so a slow Sync of a replaced file can't
	// record a stale durable length (NEBULA_CHAOS_LEDGER only).
	gen uint64
}

// NewFileStorage opens (or creates) Raft storage under dir. When sync is
// false writes are not fsynced, which is only appropriate for tests and
// benchmarks that do not simulate power loss.
func NewFileStorage(dir string, sync bool) (*FileStorage, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &FileStorage{dir: dir, sync: sync, crcTb: crc32.MakeTable(crc32.Castagnoli)}
	f, err := os.OpenFile(s.path("raft.log"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s.logF = f
	if chaosLedger {
		// Whatever survived the last crash is, by definition, on disk.
		if st, err := f.Stat(); err == nil {
			s.writeLedger(st.Size())
		}
	}
	return s, nil
}

func (s *FileStorage) path(name string) string { return filepath.Join(s.dir, name) }

func (s *FileStorage) LoadState() (uint64, string, error) {
	b, err := os.ReadFile(s.path("state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	var hs hardState
	if err := json.Unmarshal(b, &hs); err != nil {
		return 0, "", fmt.Errorf("corrupt raft state: %w", err)
	}
	return hs.Term, hs.VotedFor, nil
}

func (s *FileStorage) SaveState(term uint64, votedFor string) error {
	b, _ := json.Marshal(hardState{Term: term, VotedFor: votedFor})
	return s.writeAtomic("state.json", b)
}

// ErrCorrupt reports damaged Raft storage. A node must not start on it: a
// voter that silently forgets entries it acknowledged can help elect a
// leader that never had them, and acknowledged writes disappear.
var ErrCorrupt = errors.New("raft: storage corrupt")

// maxRecord bounds a log record's length so a flipped length field can't
// make recovery allocate gigabytes.
const maxRecord = 64 << 20

// decodeAt parses the record at off, returning it and its size on disk.
func (s *FileStorage) decodeAt(b []byte, off int) (*pb.LogEntry, int, bool) {
	if off+8 > len(b) {
		return nil, 0, false
	}
	n := int(binary.LittleEndian.Uint32(b[off:]))
	sum := binary.LittleEndian.Uint32(b[off+4:])
	if n > maxRecord || off+8+n > len(b) {
		return nil, 0, false
	}
	payload := b[off+8 : off+8+n]
	if crc32.Checksum(payload, s.crcTb) != sum {
		return nil, 0, false
	}
	e := &pb.LogEntry{}
	if err := proto.Unmarshal(payload, e); err != nil {
		return nil, 0, false
	}
	return e, 8 + n, true
}

// LoadLog reads every intact record. A crash can leave the final write
// half-done; that torn tail is cut off, as before. But a bad record with
// intact records after it is not a torn write, it is damage to data the
// node had already fsynced and acknowledged, and recovery refuses to
// continue instead of quietly dropping everything from that point on.
func (s *FileStorage) LoadLog() ([]*pb.LogEntry, error) {
	b, err := os.ReadFile(s.path("raft.log"))
	if err != nil {
		return nil, err
	}
	var out []*pb.LogEntry
	off := 0
	for off < len(b) {
		e, n, ok := s.decodeAt(b, off)
		if !ok {
			break
		}
		out = append(out, e)
		off += n
	}
	if off < len(b) {
		for p := off + 1; p+8 <= len(b); p++ {
			if _, _, ok := s.decodeAt(b, p); ok {
				return nil, fmt.Errorf("%w: raft.log is damaged at byte %d but holds intact records from byte %d on; "+
					"refusing to start rather than silently dropping entries this node may have acknowledged", ErrCorrupt, off, p)
			}
		}
		// Only a torn tail: drop it so later appends start at a record
		// boundary.
		if err := s.logF.Truncate(int64(off)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *FileStorage) encode(entries []*pb.LogEntry) ([]byte, error) {
	var out []byte
	for _, e := range entries {
		b, err := proto.Marshal(e)
		if err != nil {
			return nil, err
		}
		var hdr [8]byte
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(b)))
		binary.LittleEndian.PutUint32(hdr[4:8], crc32.Checksum(b, s.crcTb))
		out = append(out, hdr[:]...)
		out = append(out, b...)
	}
	return out, nil
}

func (s *FileStorage) AppendLog(entries []*pb.LogEntry) error {
	if err := s.AppendLogNoSync(entries); err != nil {
		return err
	}
	return s.Sync()
}

func (s *FileStorage) AppendLogNoSync(entries []*pb.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	b, err := s.encode(entries)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.logF.Write(b)
	return err
}

// Sync flushes everything written so far. It deliberately does not hold the
// lock across the fsync so appends can continue while the disk catches up.
func (s *FileStorage) Sync() error {
	if !s.sync {
		return nil
	}
	s.mu.Lock()
	f, gen := s.logF, s.gen
	var size int64 = -1
	if chaosLedger {
		if st, err := f.Stat(); err == nil {
			size = st.Size()
		}
	}
	s.mu.Unlock()
	err := f.Sync()
	if errors.Is(err, os.ErrClosed) {
		// RewriteLog replaced the file meanwhile; it fsyncs the new one,
		// which holds every entry we were asked to make durable.
		return nil
	}
	if err == nil && size >= 0 {
		s.mu.Lock()
		if s.gen == gen {
			s.writeLedger(size)
		}
		s.mu.Unlock()
	}
	return err
}

func (s *FileStorage) RewriteLog(entries []*pb.LogEntry) error {
	b, err := s.encode(entries)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeAtomic("raft.log", b); err != nil {
		return err
	}
	_ = s.logF.Close()
	f, err := os.OpenFile(s.path("raft.log"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	s.logF = f
	s.gen++
	if chaosLedger && s.sync {
		s.writeLedger(int64(len(b)))
	}
	return nil
}

// snapMagic marks the v2 snapshot format, whose checksum also covers the
// index and term. In v1 only the data was checksummed, so a flipped bit in
// the header was accepted: a snapshot claiming a huge term lets a node win
// elections it should lose.
var snapMagic = [4]byte{'N', 'S', 'N', '2'}

func (s *FileStorage) LoadSnapshot() (uint64, uint64, []byte, error) {
	b, err := os.ReadFile(s.path("snapshot.bin"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil, nil
	}
	if err != nil {
		return 0, 0, nil, err
	}
	if len(b) >= 24 && [4]byte(b[0:4]) == snapMagic {
		sum := binary.LittleEndian.Uint32(b[20:24])
		body := append(append([]byte{}, b[4:20]...), b[24:]...)
		if crc32.Checksum(body, s.crcTb) != sum {
			return 0, 0, nil, fmt.Errorf("%w: snapshot checksum mismatch", ErrCorrupt)
		}
		return binary.LittleEndian.Uint64(b[4:12]), binary.LittleEndian.Uint64(b[12:20]), b[24:], nil
	}
	// v1: [idx][term][crc(data)][data]
	if len(b) < 20 {
		return 0, 0, nil, fmt.Errorf("%w: snapshot file truncated", ErrCorrupt)
	}
	idx := binary.LittleEndian.Uint64(b[0:8])
	term := binary.LittleEndian.Uint64(b[8:16])
	sum := binary.LittleEndian.Uint32(b[16:20])
	data := b[20:]
	if crc32.Checksum(data, s.crcTb) != sum {
		return 0, 0, nil, fmt.Errorf("%w: snapshot checksum mismatch", ErrCorrupt)
	}
	return idx, term, data, nil
}

func (s *FileStorage) SaveSnapshot(index, term uint64, data []byte) error {
	b := make([]byte, 24+len(data))
	copy(b[0:4], snapMagic[:])
	binary.LittleEndian.PutUint64(b[4:12], index)
	binary.LittleEndian.PutUint64(b[12:20], term)
	copy(b[24:], data)
	body := append(append([]byte{}, b[4:20]...), data...)
	binary.LittleEndian.PutUint32(b[20:24], crc32.Checksum(body, s.crcTb))
	return s.writeAtomic("snapshot.bin", b)
}

func (s *FileStorage) writeAtomic(name string, b []byte) error {
	tmp := s.path(name + ".tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if s.sync {
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path(name)); err != nil {
		return err
	}
	if s.sync {
		// The rename itself is only durable once the directory is.
		d, err := os.Open(s.dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
	return nil
}

func (s *FileStorage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logF.Close()
}

// MemoryStorage keeps everything in RAM. Used by tests, where it also lets a
// "crashed" node restart with exactly the state it had persisted.
type MemoryStorage struct {
	term     uint64
	votedFor string
	log      []*pb.LogEntry
	snapIdx  uint64
	snapTerm uint64
	snap     []byte
}

func NewMemoryStorage() *MemoryStorage { return &MemoryStorage{} }

func (m *MemoryStorage) LoadState() (uint64, string, error) { return m.term, m.votedFor, nil }
func (m *MemoryStorage) SaveState(t uint64, v string) error {
	m.term, m.votedFor = t, v
	return nil
}
func (m *MemoryStorage) LoadLog() ([]*pb.LogEntry, error) {
	return append([]*pb.LogEntry(nil), m.log...), nil
}
func (m *MemoryStorage) AppendLog(e []*pb.LogEntry) error {
	m.log = append(m.log, e...)
	return nil
}
func (m *MemoryStorage) AppendLogNoSync(e []*pb.LogEntry) error { return m.AppendLog(e) }
func (m *MemoryStorage) Sync() error                            { return nil }
func (m *MemoryStorage) RewriteLog(e []*pb.LogEntry) error {
	m.log = append([]*pb.LogEntry(nil), e...)
	return nil
}
func (m *MemoryStorage) LoadSnapshot() (uint64, uint64, []byte, error) {
	return m.snapIdx, m.snapTerm, m.snap, nil
}
func (m *MemoryStorage) SaveSnapshot(i, t uint64, d []byte) error {
	m.snapIdx, m.snapTerm, m.snap = i, t, append([]byte(nil), d...)
	return nil
}
func (m *MemoryStorage) Close() error { return nil }
