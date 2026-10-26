package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coolstack-coder/nebulachaos/internal/checker"
	"github.com/coolstack-coder/nebulachaos/internal/history"
	"github.com/coolstack-coder/nebulachaos/internal/nemesis"
	"github.com/coolstack-coder/nebulachaos/internal/runner"
)

func parseSeeds(s string) ([]uint64, error) {
	var out []uint64
	for _, part := range strings.Split(s, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.ParseUint(lo, 10, 64)
		if err != nil {
			return nil, err
		}
		b := a
		if isRange {
			if b, err = strconv.ParseUint(hi, 10, 64); err != nil {
				return nil, err
			}
		}
		for x := a; x <= b; x++ {
			out = append(out, x)
		}
	}
	return out, nil
}

type campaignRow struct {
	Profile string          `json:"profile"`
	Summary *runner.Summary `json:"summary"`
}

func cmdCampaign(args []string) error {
	fs := flag.NewFlagSet("campaign", flag.ExitOnError)
	var c common
	c.register(fs)
	profiles := fs.String("profiles", "partitions,pauses,crashes,disk,mixed", "comma-separated profiles")
	seedsArg := fs.String("seeds", "1-5", "seeds, e.g. 1-20 or 3,7,9")
	schedule := fs.String("schedule", "", "run this fixed schedule for every seed instead of generated ones")
	fs.Parse(args)
	var fixed *nemesis.Schedule
	if *schedule != "" {
		var err error
		if fixed, err = nemesis.Load(*schedule); err != nil {
			return err
		}
		*profiles = fixed.Name
	}
	seeds, err := parseSeeds(*seedsArg)
	if err != nil {
		return fmt.Errorf("--seeds: %w", err)
	}
	bin, err := c.binary()
	if err != nil {
		return err
	}
	ctx, cancel := interruptible()
	defer cancel()
	stamp := time.Now().Format("20060102-150405")
	label := strings.NewReplacer("/", "-").Replace(c.target)
	if c.bugs != "" {
		label += "+" + c.bugs
	}
	dir := filepath.Join(c.out, "campaign-"+label+"-"+stamp)
	c.out = dir
	var rows []campaignRow
	start := time.Now()
	total := len(seeds) * len(strings.Split(*profiles, ","))
	fmt.Printf("%scampaign: %d runs against %s, %v of faults each%s\n", bold, total, c.target, c.duration, reset)
	n := 0
	for _, p := range strings.Split(*profiles, ",") {
		for _, seed := range seeds {
			if ctx.Err() != nil {
				break
			}
			n++
			s := fixed
			if s == nil {
				if s, err = nemesis.Generate(seed, p, c.duration); err != nil {
					return err
				}
			}
			sum, err := runOne(ctx, &c, bin, s, seed, false)
			if err != nil {
				fmt.Printf("  [%3d/%d] %-11s seed %-4d %serror: %v%s\n", n, total, p, seed, red, err, reset)
				continue
			}
			rows = append(rows, campaignRow{p, sum})
			verdict := green + "✓ linearizable" + reset
			if !sum.Valid {
				verdict = red + "✗ " + strings.Join(sum.Anomalies, ", ") + reset
			} else if sum.Unknown {
				verdict = amber + "? unknown" + reset
			}
			refused := ""
			if len(sum.Refused) > 0 {
				refused = fmt.Sprintf("  %s(%d node refused to start)%s", amber, len(sum.Refused), reset)
			}
			fmt.Printf("  [%3d/%d] %-11s seed %-4d %6d ops %3d faults  %s%s\n", n, total, p, seed, sum.Ops, sum.Faults, verdict, refused)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "results.json"), b, 0o644)
	md := campaignMarkdown(c.target, c.bugs, rows, time.Since(start))
	_ = os.WriteFile(filepath.Join(dir, "results.md"), []byte(md), 0o644)
	fmt.Println()
	fmt.Print(md)
	fmt.Printf("\n%sresults: %s%s\n", dim, dir, reset)
	return nil
}

func campaignMarkdown(target, bugs string, rows []campaignRow, took time.Duration) string {
	type agg struct {
		runs, ops, failed, faults, refused int
		anomalies                          map[string]int
	}
	by := map[string]*agg{}
	var order []string
	stalls := map[string][]float64{}
	for _, r := range rows {
		a := by[r.Profile]
		if a == nil {
			a = &agg{anomalies: map[string]int{}}
			by[r.Profile] = a
			order = append(order, r.Profile)
		}
		a.runs++
		a.ops += r.Summary.Ops
		a.faults += r.Summary.Faults
		if !r.Summary.Valid {
			a.failed++
			for _, an := range r.Summary.Anomalies {
				_, kind, _ := strings.Cut(an, ": ")
				a.anomalies[kind]++
			}
		}
		if len(r.Summary.Refused) > 0 {
			a.refused++
		}
		for _, s := range r.Summary.Stalls {
			stalls[s.Fault] = append(stalls[s.Fault], s.Stall)
		}
	}
	var b strings.Builder
	t := target
	if bugs != "" {
		t += " with planted bug " + bugs
	}
	fmt.Fprintf(&b, "### Campaign against `%s`\n\n", t)
	b.WriteString("| profile | runs | operations | faults | non-linearizable runs | anomalies (keys) | runs where a node refused to start |\n|---|---|---|---|---|---|---|\n")
	var tr, to, tf, tbad int
	for _, p := range order {
		a := by[p]
		var an []string
		for k, v := range a.anomalies {
			an = append(an, fmt.Sprintf("%s ×%d", k, v))
		}
		sort.Strings(an)
		fmt.Fprintf(&b, "| %s | %d | %d | %d | **%d** | %s | %d |\n", p, a.runs, a.ops, a.faults, a.failed, strings.Join(an, ", "), a.refused)
		tr, to, tf, tbad = tr+a.runs, to+a.ops, tf+a.faults, tbad+a.failed
	}
	fmt.Fprintf(&b, "| **total** | **%d** | **%d** | **%d** | **%d** | | |\n\n", tr, to, tf, tbad)
	if len(stalls) > 0 {
		b.WriteString("Write availability: longest gap between successful writes in the 10 s after each fault.\n\n| fault | count | median stall | worst stall |\n|---|---|---|---|\n")
		var ks []string
		for k := range stalls {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			v := stalls[k]
			sort.Float64s(v)
			fmt.Fprintf(&b, "| %s | %d | %.2f s | %.2f s |\n", k, len(v), v[len(v)/2], v[len(v)-1])
		}
	}
	fmt.Fprintf(&b, "\n_%d runs in %s._\n", tr, took.Round(time.Second))
	return b.String()
}

// cmdRecheck re-runs the current checker over every history a campaign
// recorded and rewrites its results, so published tables always come from
// the latest checker.
func cmdRecheck(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: nebulachaos recheck runs/campaign-…")
	}
	dir := args[0]
	b, err := os.ReadFile(filepath.Join(dir, "results.json"))
	if err != nil {
		return err
	}
	var rows []campaignRow
	if err := json.Unmarshal(b, &rows); err != nil {
		return err
	}
	changed := 0
	for _, r := range rows {
		s := r.Summary
		ops, err := history.Load(filepath.Join(s.Dir, "history.jsonl"))
		if err != nil {
			return err
		}
		rep := checker.Check(ops, 3_000_000)
		var an []string
		for _, k := range rep.Keys {
			if k.Anomaly != "" {
				an = append(an, fmt.Sprintf("%s: %s", k.Key, k.Anomaly))
			}
		}
		if rep.Valid != s.Valid || strings.Join(an, ",") != strings.Join(s.Anomalies, ",") {
			changed++
			fmt.Printf("  %s: %v %v → %v %v\n", filepath.Base(s.Dir), s.Valid, s.Anomalies, rep.Valid, an)
		}
		s.Valid, s.Unknown, s.Anomalies, s.Agree = rep.Valid, rep.Unknown, an, rep.Agree
		writeFile(filepath.Join(s.Dir, "check.json"), rep)
		writeFile(filepath.Join(s.Dir, "summary.json"), s)
	}
	out, _ := json.MarshalIndent(rows, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "results.json"), out, 0o644)
	target, bugs := "", ""
	if len(rows) > 0 {
		target, bugs = rows[0].Summary.Target, rows[0].Summary.Bugs
	}
	md := campaignMarkdown(target, bugs, rows, 0)
	md = strings.Replace(md, " in 0s._", " (re-checked)._", 1)
	_ = os.WriteFile(filepath.Join(dir, "results.md"), []byte(md), 0o644)
	fmt.Printf("re-checked %d runs, %d verdicts or labels changed\n", len(rows), changed)
	return nil
}

func writeFile(path string, v any) {
	if b, err := json.MarshalIndent(v, "", "  "); err == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
}

// cmdReport re-renders a run's HTML report from its saved files.
func cmdReport(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: nebulachaos report runs/<run>")
	}
	dir := args[0]
	var sum runner.Summary
	var faults []nemesis.Record
	for name, v := range map[string]any{"summary.json": &sum, "nemesis.json": &faults} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, v); err != nil {
			return err
		}
	}
	ops, err := history.Load(filepath.Join(dir, "history.jsonl"))
	if err != nil {
		return err
	}
	rep := checker.Check(ops, 3_000_000)
	writeFile(filepath.Join(dir, "check.json"), rep)
	out := filepath.Join(dir, "timeline.html")
	if err := runner.WriteTimeline(out, &sum, ops, faults, rep); err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
