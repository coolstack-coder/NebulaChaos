package nemesis

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
)

// Profiles name the fault mixes Generate can draw from.
var Profiles = map[string][]string{
	"partitions": {"isolate-leader", "leader-alone", "halves", "ring", "bridge", "delay"},
	"pauses":     {"pause-leader", "pause-follower"},
	"crashes":    {"kill-leader", "kill-follower", "powerloss-node"},
	"disk":       {"powerloss-node", "powerloss-all", "flip-log", "corrupt-snapshot", "snapshot-header", "delete-snapshot"},
	"mixed": {"isolate-leader", "leader-alone", "ring", "bridge", "halves", "pause-leader", "pause-follower",
		"kill-leader", "kill-follower", "powerloss-node", "powerloss-all", "flip-log", "snapshot-header", "delete-snapshot"},
}

// ProfileNames lists the profiles in a stable order.
func ProfileNames() []string {
	var out []string
	for k := range Profiles {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Generate builds a random but fully reproducible schedule: fault windows
// of 1–5 s separated by quiet gaps of 1–3 s (long enough to elect a leader,
// so liveness gets a chance too). At most one disk-corruption fault is
// drawn per run: Raft tolerates f failures only if they are fail-stop, and
// corrupting two of five disks would test the arithmetic, not the code.
func Generate(seed uint64, profile string, duration time.Duration) (*Schedule, error) {
	mix, ok := Profiles[profile]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q (have %s)", profile, strings.Join(ProfileNames(), ", "))
	}
	r := rand.New(rand.NewPCG(seed, 0x7363686564))
	s := &Schedule{
		Name:        fmt.Sprintf("%s-%d", profile, seed),
		Description: fmt.Sprintf("generated: profile %s, seed %d", profile, seed),
		Duration:    Dur(duration),
		Seed:        seed,
	}
	ms := func(lo, hi int) time.Duration { return time.Duration(lo+r.IntN(hi-lo+1)) * time.Millisecond }
	t := ms(1500, 2500)
	usedDisk := false
	for t < duration-3*time.Second {
		kind := mix[r.IntN(len(mix))]
		switch kind {
		case "flip-log", "corrupt-snapshot", "snapshot-header", "delete-snapshot":
			if usedDisk {
				continue
			}
			usedDisk = true
		}
		hold := ms(1000, 5000)
		ev := Event{At: Dur(t)}
		switch kind {
		case "isolate-leader", "leader-alone", "halves", "ring", "bridge":
			ev.Do, ev.Target, ev.For = "partition", kind, Dur(hold)
		case "delay":
			ev.Do, ev.Target, ev.Delay, ev.For = "delay", "all", Dur(ms(50, 400)), Dur(hold)
		case "pause-leader":
			ev.Do, ev.Target, ev.For = "pause", "leader", Dur(hold)
		case "pause-follower":
			ev.Do, ev.Target, ev.For = "pause", "follower", Dur(hold)
		case "kill-leader":
			ev.Do, ev.Target, ev.For = "kill", "leader", Dur(hold)
		case "kill-follower":
			ev.Do, ev.Target, ev.For = "kill", "follower", Dur(hold)
		case "powerloss-node":
			ev.Do, ev.Target = "powerloss", "random"
			hold = 0
		case "powerloss-all":
			ev.Do, ev.Target = "powerloss", "all"
			hold = 0
		default:
			ev.Do, ev.Target = kind, "random"
			hold = 0
		}
		s.Events = append(s.Events, ev)
		t += hold + ms(1000, 3000)
	}
	return s, nil
}
