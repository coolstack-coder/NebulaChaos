package raft

import (
	"os"
	"strconv"
	"strings"
)

// Hooks for NebulaChaos, the fault-injection test suite. Everything here is
// inert unless enabled through the environment of the node process.
//
//	NEBULA_CHAOS_LEDGER=1  record, next to raft.log, how many bytes of it
//	                       have really been fsynced. The tester simulates a
//	                       power cut by killing the process and truncating
//	                       the log to that length: page cache survives
//	                       SIGKILL, so a plain kill never loses unsynced data.
//	NEBULA_CHAOS_BUGS=a,b  deliberately break the node (see chaosBugs) so
//	                       the tester can prove it catches real mistakes.
var (
	chaosLedger = os.Getenv("NEBULA_CHAOS_LEDGER") != ""
	chaosBugs   = parseChaosBugs(os.Getenv("NEBULA_CHAOS_BUGS"))
)

// Planted bugs understood by this package.
const (
	// The leader counts its own log as durable without fsyncing it.
	BugLeaderSkipsFsync = "leader-skips-fsync"
	// The leader acknowledges a write as soon as it is in its own log,
	// before a majority has it.
	BugAckBeforeCommit = "ack-before-commit"
	// The vote is not persisted, so a restarted node can vote twice in a
	// term.
	BugForgetVote = "forget-vote"
	// The leader serves reads from its own state without confirming it is
	// still the leader (the etcd/Consul stale-read bug from 2014).
	BugLeaderLocalReads = "leader-local-reads"
)

func parseChaosBugs(s string) map[string]bool {
	out := map[string]bool{}
	for _, b := range strings.Split(s, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out[b] = true
		}
	}
	return out
}

func chaosBug(name string) bool { return chaosBugs[name] }

// writeLedger records that the first size bytes of raft.log are on disk.
// It is atomic with respect to process crashes, which is all it needs.
func (s *FileStorage) writeLedger(size int64) {
	tmp := s.path("durable.ledger.tmp")
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(size, 10)), 0o644); err == nil {
		_ = os.Rename(tmp, s.path("durable.ledger"))
	}
}
