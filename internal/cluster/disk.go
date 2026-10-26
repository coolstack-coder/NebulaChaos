package cluster

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Disk faults are applied to a node that is not running, between a crash
// and its restart, by editing its Raft files the way failing hardware would.

func (c *Cluster) raftFile(i int, name string) string {
	return filepath.Join(c.DataDir(i), "raft", name)
}

// records returns the byte offset of every record in raft.log
// ([u32 len][u32 crc][payload]).
func records(b []byte) []int {
	var offs []int
	for off := 0; off+8 <= len(b); {
		n := int(binary.LittleEndian.Uint32(b[off:]))
		if off+8+n > len(b) {
			break
		}
		offs = append(offs, off)
		off += 8 + n
	}
	return offs
}

// PowerLoss simulates pulling the plug on node i: the process dies and
// everything not yet fsynced is lost. NebulaOS (run with
// NEBULA_CHAOS_LEDGER) records how much of its log really reached the disk;
// the rest is cut off, and with tear the cut lands at a random byte inside
// the unsynced region, the way a half-written sector would.
func (c *Cluster) PowerLoss(i int, r *rand.Rand, tear bool) (string, error) {
	c.Kill(i)
	logPath := c.raftFile(i, "raft.log")
	st, err := os.Stat(logPath)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(c.raftFile(i, "durable.ledger"))
	durable := int64(0)
	if err == nil {
		durable, _ = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	}
	size := st.Size()
	if durable > size {
		durable = size
	}
	cut := durable
	if tear && size > durable {
		cut += r.Int64N(size - durable)
	}
	if err := os.Truncate(logPath, cut); err != nil {
		return "", err
	}
	return fmt.Sprintf("log %d → %d bytes (%d fsynced)", size, cut, durable), nil
}

// FlipLogBit flips one bit in the middle of a record that is not the last
// one: bit rot in data the node had fsynced and acknowledged.
func (c *Cluster) FlipLogBit(i int, r *rand.Rand) (string, error) {
	p := c.raftFile(i, "raft.log")
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	offs := records(b)
	if len(offs) < 3 {
		return "", errors.New("log too short to corrupt")
	}
	k := r.IntN(len(offs) - 1) // never the final record: that would look like a torn write
	n := int(binary.LittleEndian.Uint32(b[offs[k]:]))
	at := offs[k] + 8 + r.IntN(n)
	b[at] ^= 1 << r.IntN(8)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("flipped a bit in record %d/%d of raft.log (byte %d)", k+1, len(offs), at), nil
}

// CorruptSnapshot flips a bit in snapshot.bin. header picks the 16-byte
// index/term header, which upstream NebulaOS does not checksum.
func (c *Cluster) CorruptSnapshot(i int, r *rand.Rand, header bool) (string, error) {
	p := c.raftFile(i, "snapshot.bin")
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("no snapshot yet: %w", err)
	}
	if len(b) < 21 {
		return "", errors.New("snapshot too short")
	}
	var at int
	if header {
		at = 8 + r.IntN(3) // low bytes of the term: plausible-looking garbage
	} else {
		at = 20 + r.IntN(len(b)-20)
	}
	b[at] ^= 1 << r.IntN(8)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", err
	}
	where := "body"
	if header {
		where = "header (term)"
	}
	return fmt.Sprintf("flipped a bit in snapshot.bin %s at byte %d", where, at), nil
}

// DeleteSnapshot removes snapshot.bin while keeping the compacted log, as a
// lost rename after a crash would.
func (c *Cluster) DeleteSnapshot(i int) (string, error) {
	p := c.raftFile(i, "snapshot.bin")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("no snapshot yet: %w", err)
	}
	return "deleted snapshot.bin (compacted log kept)", os.Remove(p)
}
