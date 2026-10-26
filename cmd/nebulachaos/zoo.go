package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coolstack-coder/nebulachaos/internal/nemesis"
)

// A checker that never fails is useless, and you can't tell it apart from
// a correct system. The zoo plants known bugs in NebulaOS (behind
// NEBULA_CHAOS_BUGS) and requires the tester to catch each one, with a
// control run of the same schedule without the bug that must pass.
type zooCase struct {
	bug, schedule, why string
	clients, keys      int
	timeout            time.Duration
}

var zooCases = []zooCase{
	{"leader-local-reads", "schedules/leader-alone-repeated.yaml",
		"leader answers reads without confirming it still leads", 20, 2, 500 * time.Millisecond},
	{"ack-before-commit", "schedules/isolate-leader.yaml",
		"leader acknowledges a write before a majority has it", 12, 4, time.Second},
	{"leader-skips-fsync", "schedules/powerloss-cluster.yaml",
		"leader counts its own log as durable without fsync", 12, 4, time.Second},
	{"forget-vote", "schedules/crash-elections.yaml",
		"vote is not persisted; a restarted node can vote twice in a term", 12, 4, time.Second},
}

func cmdZoo(args []string) error {
	fs := flag.NewFlagSet("zoo", flag.ExitOnError)
	var c common
	c.register(fs)
	attempts := fs.Int("attempts", 3, "seeds to try per bug before giving up")
	only := fs.String("only", "", "run just these bugs (comma-separated)")
	fs.Parse(args)
	bin, err := c.binary()
	if err != nil {
		return err
	}
	ctx, cancel := interruptible()
	defer cancel()
	dir := filepath.Join(c.out, "zoo-"+time.Now().Format("20060102-150405"))
	c.out = dir
	var b strings.Builder
	b.WriteString("| planted bug | what it breaks | fault schedule | control run (no bug) | caught? | anomaly | ops checked |\n|---|---|---|---|---|---|---|\n")
	for _, z := range zooCases {
		if *only != "" && !strings.Contains(","+*only+",", ","+z.bug+",") {
			continue
		}
		s, err := nemesis.Load(z.schedule)
		if err != nil {
			return err
		}
		fmt.Printf("%s● %s%s — %s\n", bold, z.bug, reset, z.why)
		cc := c
		cc.clients, cc.keys, cc.timeout, cc.bugs = z.clients, z.keys, z.timeout, ""
		ctl, err := runOne(ctx, &cc, bin, s, 1, false)
		if err != nil {
			return err
		}
		control := "✓ linearizable"
		if !ctl.Valid {
			control = "✗ " + strings.Join(ctl.Anomalies, ", ")
		}
		fmt.Printf("    control (no bug): %s\n", control)
		cc.bugs = z.bug
		caught, anomaly, ops := "no", "—", 0
		for seed := uint64(1); seed <= uint64(*attempts) && ctx.Err() == nil; seed++ {
			sum, err := runOne(ctx, &cc, bin, s, seed, false)
			if err != nil {
				return err
			}
			ops += sum.Ops
			if !sum.Valid {
				caught = fmt.Sprintf("**yes**, attempt %d", seed)
				anomaly = strings.Join(uniqueKinds(sum.Anomalies), ", ")
				fmt.Printf("    %sseed %d: caught — %s%s\n      %s\n", red, seed, anomaly, reset, filepath.Join(sum.Dir, "timeline.html"))
				break
			}
			fmt.Printf("    %sseed %d: not caught (%d ops)%s\n", dim, seed, sum.Ops, reset)
		}
		if caught == "no" {
			caught = fmt.Sprintf("not in %d attempts", *attempts)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %d |\n", z.bug, z.why, s.Name, control, caught, anomaly, ops)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(dir, "zoo.md"), []byte(b.String()), 0o644)
	fmt.Println()
	fmt.Print(b.String())
	return nil
}

func uniqueKinds(an []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range an {
		_, k, _ := strings.Cut(a, ": ")
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}
