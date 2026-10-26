package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/coolstack-coder/nebulachaos/internal/checker"
	"github.com/coolstack-coder/nebulachaos/internal/history"
	"github.com/coolstack-coder/nebulachaos/internal/nemesis"
	"github.com/coolstack-coder/nebulachaos/internal/runner"
)

const (
	bold  = "\033[1m"
	red   = "\033[31m"
	green = "\033[32m"
	amber = "\033[33m"
	dim   = "\033[2m"
	reset = "\033[0m"
)

type common struct {
	target   string
	bin      string
	out      string
	clients  int
	keys     int
	bugs     string
	snap     int
	duration time.Duration
	timeout  time.Duration
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.target, "target", "HEAD", "git ref of target/nebulaos to test (e.g. nebulaos-baseline, nebulaos-fixed, HEAD, worktree)")
	fs.StringVar(&c.bin, "bin", "", "use this nebula binary instead of building --target")
	fs.StringVar(&c.out, "out", "runs", "directory for run results")
	fs.IntVar(&c.clients, "clients", 10, "concurrent clients")
	fs.IntVar(&c.keys, "keys", 4, "registers (keys) the clients share")
	fs.StringVar(&c.bugs, "bugs", "", "planted bugs to switch on in every node (NEBULA_CHAOS_BUGS)")
	fs.IntVar(&c.snap, "snapshot-every", 64, "NebulaOS --snapshot-every; small values exercise snapshots and InstallSnapshot")
	fs.DurationVar(&c.duration, "duration", 30*time.Second, "fault phase length for generated schedules")
	fs.DurationVar(&c.timeout, "timeout", time.Second, "client request timeout (a timed-out write is recorded as indeterminate)")
}

func root() string {
	wd, _ := os.Getwd()
	for d := wd; d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "target", "nebulaos", "go.mod")); err == nil {
			return d
		}
	}
	return wd
}

func (c *common) binary() (string, error) {
	if c.bin != "" {
		return c.bin, nil
	}
	fmt.Printf("%sbuilding nebula at %s…%s\n", dim, c.target, reset)
	return runner.Build(root(), c.target)
}

func interruptible() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var c common
	c.register(fs)
	schedule := fs.String("schedule", "", "fault schedule YAML")
	profile := fs.String("profile", "mixed", "generated schedule profile: "+strings.Join(nemesis.ProfileNames(), ", "))
	seed := fs.Uint64("seed", 0, "seed for the generated schedule and the workload (0 = random)")
	fs.Parse(args)
	if *seed == 0 {
		*seed = rand.Uint64N(1e6)
	}
	var s *nemesis.Schedule
	var err error
	if *schedule != "" {
		s, err = nemesis.Load(*schedule)
	} else {
		s, err = nemesis.Generate(*seed, *profile, c.duration)
	}
	if err != nil {
		return err
	}
	bin, err := c.binary()
	if err != nil {
		return err
	}
	ctx, cancel := interruptible()
	defer cancel()
	sum, err := runOne(ctx, &c, bin, s, *seed, true)
	if err != nil {
		return err
	}
	if !sum.Valid {
		os.Exit(3)
	}
	return nil
}

func runDir(c *common, s *nemesis.Schedule, seed uint64) string {
	label := strings.NewReplacer("/", "-", "^", "").Replace(c.target)
	if c.bugs != "" {
		label += "+" + strings.ReplaceAll(c.bugs, ",", "+")
	}
	return filepath.Join(c.out, fmt.Sprintf("%s-%s-seed%d-%s", label, s.Name, seed, time.Now().Format("150405")))
}

func runOne(ctx context.Context, c *common, bin string, s *nemesis.Schedule, seed uint64, verbose bool) (*runner.Summary, error) {
	dir := runDir(c, s, seed)
	logf := func(f string, a ...any) {
		if verbose {
			fmt.Printf("%s[%s] %s%s\n", dim, time.Now().Format("15:04:05"), fmt.Sprintf(f, a...), reset)
		}
	}
	if verbose {
		fmt.Printf("%s▶ %s%s  target=%s seed=%d clients=%d keys=%d%s\n", bold, s.Name, reset, c.target, seed, c.clients, c.keys, bugTag(c.bugs))
		for _, e := range s.Events {
			extra := ""
			if e.For > 0 {
				extra = fmt.Sprintf(" for %v", e.For.D())
			}
			fmt.Printf("%s  %6.1fs  %-16s %s%s%s\n", dim, e.At.D().Seconds(), e.Do, e.Target, extra, reset)
		}
	}
	sum, err := runner.Run(ctx, runner.Options{
		Bin: bin, Target: c.target, Out: dir, Schedule: s, Seed: seed,
		Clients: c.clients, Keys: c.keys, Timeout: c.timeout, Bugs: c.bugs, SnapEach: c.snap, Logf: logf,
	})
	if err != nil {
		return nil, err
	}
	if verbose {
		printSummary(sum)
	}
	return sum, nil
}

func bugTag(b string) string {
	if b == "" {
		return ""
	}
	return fmt.Sprintf("  %splanted bug: %s%s", amber, b, reset)
}

func printSummary(s *runner.Summary) {
	fmt.Printf("\n  %d operations  (%d ok, %d failed, %d indeterminate)  ·  %d faults  ·  checked in %dms\n",
		s.Ops, s.OK, s.Fail, s.Info, s.Faults, s.CheckMillis)
	for n, e := range s.Refused {
		fmt.Printf("  %s%s refused to start: %s%s\n", amber, n, e, reset)
	}
	switch {
	case !s.Valid:
		fmt.Printf("  %s%s✗ NOT LINEARIZABLE%s  %s\n", bold, red, reset, strings.Join(s.Anomalies, "; "))
	case s.Unknown:
		fmt.Printf("  %s%s? UNKNOWN%s (search budget exhausted)\n", bold, amber, reset)
	default:
		fmt.Printf("  %s%s✓ linearizable%s\n", bold, green, reset)
	}
	if !s.Agree {
		fmt.Printf("  %s!! checkers disagree%s\n", red, reset)
	}
	fmt.Printf("  %sreport: %s%s\n\n", dim, filepath.Join(s.Dir, "timeline.html"), reset)
}

func cmdCheck(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: nebulachaos check history.jsonl")
	}
	ops, err := history.Load(args[0])
	if err != nil {
		return err
	}
	rep := checker.Check(ops, 0)
	for _, k := range rep.Keys {
		fmt.Printf("%-6s %5d ops  %s\n", k.Key, k.Ops, k.Result)
		if k.Anomaly != "" {
			fmt.Printf("  %s%s%s\n", red, k.Anomaly, reset)
			for _, l := range k.Explain {
				fmt.Println("   ", l)
			}
		}
	}
	fmt.Printf("porcupine: %s, agree: %v\n", rep.Porcupine, rep.Agree)
	if !rep.Valid {
		os.Exit(3)
	}
	return nil
}

func cmdSelftest(args []string) error {
	fs := flag.NewFlagSet("selftest", flag.ExitOnError)
	n := fs.Int("n", 10000, "random histories")
	fs.Parse(args)
	r := rand.New(rand.NewPCG(1, 2))
	start := time.Now()
	var bad, agree int
	for i := 0; i < *n; i++ {
		h := checker.RandomHistory(r, 2+r.IntN(6), 1+r.IntN(10), 0.5)
		a, _ := checker.CheckKey(h, 0)
		b, _ := checker.CheckPorcupine(h, 10*time.Second)
		if a == b {
			agree++
		} else {
			fmt.Printf("%sdisagreement on history %d: wgl=%v porcupine=%v%s\n", red, i, a, b, reset)
		}
		if a == checker.Violation {
			bad++
		}
	}
	fmt.Printf("%d random histories (%d non-linearizable): WGL and Porcupine agree on %d/%d  [%v]\n",
		*n, bad, agree, *n, time.Since(start).Round(time.Millisecond))
	if agree != *n {
		os.Exit(1)
	}
	return nil
}
