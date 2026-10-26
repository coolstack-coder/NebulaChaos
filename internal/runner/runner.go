// Package runner executes one test: start a cluster, run the workload and
// the nemesis together, heal, let the cluster settle, then check the
// history and write everything to a run directory.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/coolstack-coder/nebulachaos/internal/checker"
	"github.com/coolstack-coder/nebulachaos/internal/cluster"
	"github.com/coolstack-coder/nebulachaos/internal/history"
	"github.com/coolstack-coder/nebulachaos/internal/nemesis"
	"github.com/coolstack-coder/nebulachaos/internal/workload"
)

type Options struct {
	Bin      string
	Target   string // label for reports, e.g. the git ref
	Out      string // run directory (created)
	Schedule *nemesis.Schedule
	Seed     uint64
	Clients  int
	Keys     int
	Timeout  time.Duration
	Bugs     string // NEBULA_CHAOS_BUGS for every node
	SnapEach int
	Settle   time.Duration // healthy tail after the final heal
	Budget   int           // checker search budget per key (0 = unlimited)
	Logf     func(format string, args ...any)
}

// Stall is how long writes stopped succeeding after a fault began.
type Stall struct {
	Fault  string  `json:"fault"`
	Detail string  `json:"detail"`
	At     float64 `json:"at_s"`
	Stall  float64 `json:"write_stall_s"`
}

// Summary is the one-file result of a run.
type Summary struct {
	Name        string            `json:"name"`
	Target      string            `json:"target"`
	Bugs        string            `json:"bugs,omitempty"`
	Seed        uint64            `json:"seed"`
	StartUnixMs int64             `json:"start_unix_ms"`
	Duration    float64           `json:"duration_s"`
	Valid       bool              `json:"valid"`
	Unknown     bool              `json:"unknown"`
	Anomalies   []string          `json:"anomalies,omitempty"`
	Ops         int               `json:"ops"`
	OK          int               `json:"ok"`
	Fail        int               `json:"fail"`
	Info        int               `json:"info"`
	Faults      int               `json:"faults"`
	Stalls      []Stall           `json:"stalls,omitempty"`
	Refused     map[string]string `json:"refused_to_start,omitempty"`
	Agree       bool              `json:"checkers_agree"`
	CheckMillis int64             `json:"check_ms"`
	Dir         string            `json:"dir"`
}

func (o *Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

func Run(ctx context.Context, o Options) (*Summary, error) {
	if o.Settle == 0 {
		o.Settle = 4 * time.Second
	}
	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return nil, err
	}
	s := o.Schedule
	if err := s.Save(filepath.Join(o.Out, "schedule.yaml")); err != nil {
		return nil, err
	}
	var env []string
	if o.Bugs != "" {
		env = append(env, "NEBULA_CHAOS_BUGS="+o.Bugs)
	}
	c, err := cluster.New(cluster.Config{Bin: o.Bin, Dir: o.Out, Env: env, SnapEach: o.SnapEach})
	if err != nil {
		return nil, err
	}
	defer c.Stop()
	if err := c.StartAll(); err != nil {
		return nil, err
	}
	wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	leader, err := c.WaitLeader(wctx)
	cancel()
	if err != nil {
		return nil, err
	}
	o.logf("cluster up, leader %s", cluster.ID(leader))

	start := time.Now()
	var nodes, apis []string
	for i := 1; i <= c.N(); i++ {
		nodes = append(nodes, cluster.ID(i))
		apis = append(apis, c.APIAddr(i))
	}
	wl := workload.New(workload.Config{Nodes: nodes, APIs: apis, Clients: o.Clients, Keys: o.Keys, Timeout: o.Timeout, Seed: o.Seed}, start)
	nem := nemesis.New(c, o.Seed, start)

	runCtx, stopRun := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { wl.Run(runCtx); close(done) }()

	faultCtx, stopFaults := context.WithTimeout(ctx, s.Duration.D())
	nem.Run(faultCtx, s)
	<-faultCtx.Done()
	stopFaults()
	nem.Finish()
	o.logf("faults over, healing; settling for %v", o.Settle)
	hctx, hcancel := context.WithTimeout(ctx, 15*time.Second)
	_, _ = c.WaitLeader(hctx)
	hcancel()
	select {
	case <-time.After(o.Settle):
	case <-ctx.Done():
	}
	stopRun()
	<-done
	elapsed := time.Since(start)

	ops := wl.History()
	faults := nem.Log()
	if err := history.Save(filepath.Join(o.Out, "history.jsonl"), ops); err != nil {
		return nil, err
	}
	writeJSON(filepath.Join(o.Out, "nemesis.json"), faults)

	o.logf("checking %d operations", len(ops))
	rep := checker.Check(ops, o.Budget)
	writeJSON(filepath.Join(o.Out, "check.json"), rep)
	if !rep.Valid {
		if _, info := checker.CheckPorcupine(ops, time.Minute); true {
			_ = checker.Visualize(filepath.Join(o.Out, "porcupine.html"), info)
		}
	}

	sum := &Summary{
		Name: s.Name, Target: o.Target, Bugs: o.Bugs, Seed: o.Seed, StartUnixMs: start.UnixMilli(),
		Duration: elapsed.Seconds(), Valid: rep.Valid, Unknown: rep.Unknown,
		Ops: rep.Ops, OK: rep.OK, Fail: rep.Fail, Info: rep.Info,
		Faults: len(faults), Agree: rep.Agree, CheckMillis: rep.CheckTime.Milliseconds(), Dir: o.Out,
		Stalls: stalls(ops, faults),
	}
	for _, k := range rep.Keys {
		if k.Anomaly != "" {
			sum.Anomalies = append(sum.Anomalies, fmt.Sprintf("%s: %s", k.Key, k.Anomaly))
		}
	}
	if errs := c.StartErrors(); len(errs) > 0 {
		sum.Refused = map[string]string{}
		for i, e := range errs {
			sum.Refused[cluster.ID(i)] = e
		}
	}
	writeJSON(filepath.Join(o.Out, "summary.json"), sum)
	if err := WriteTimeline(filepath.Join(o.Out, "timeline.html"), sum, ops, faults, rep); err != nil {
		o.logf("timeline: %v", err)
	}
	return sum, nil
}

// stalls measures, for every fault, the longest gap between successful
// writes in the window from the fault until 10 s later.
func stalls(ops []history.Op, faults []nemesis.Record) []Stall {
	var done []int64
	for _, o := range ops {
		if o.Mutates() && o.Status == history.OK {
			done = append(done, o.Complete)
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i] < done[j] })
	var out []Stall
	for _, f := range faults {
		if f.Do == "heal" || f.Do == "resume" || f.Do == "start" {
			continue
		}
		from, to := f.At, f.At+10e9
		prev, worst := from, int64(0)
		for _, t := range done {
			if t < from {
				continue
			}
			if t > to {
				break
			}
			worst = max(worst, t-prev)
			prev = t
		}
		out = append(out, Stall{Fault: f.Do, Detail: f.Detail, At: float64(f.At) / 1e9, Stall: float64(worst) / 1e9})
	}
	return out
}

func writeJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
}
