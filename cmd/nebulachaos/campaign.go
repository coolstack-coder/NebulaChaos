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
