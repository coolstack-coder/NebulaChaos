package nemesis

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/coolstack-coder/nebulachaos/internal/cluster"
)

// Record is a fault as it actually happened, with selectors resolved.
type Record struct {
	At     int64  `json:"at"`  // ns since run start
	End    int64  `json:"end"` // when it was undone (0 = until the final heal)
	Do     string `json:"do"`
	Nodes  []int  `json:"nodes,omitempty"`
	Detail string `json:"detail"`
	Error  string `json:"error,omitempty"`
}

// Nemesis executes a schedule against a cluster.
type Nemesis struct {
	c     *cluster.Cluster
	r     *rand.Rand
	start time.Time

	mu  sync.Mutex
	log []Record
	wg  sync.WaitGroup
}

func New(c *cluster.Cluster, seed uint64, start time.Time) *Nemesis {
	return &Nemesis{c: c, r: rand.New(rand.NewPCG(seed, 0x6e656d65)), start: start}
}

func (n *Nemesis) now() int64 { return time.Since(n.start).Nanoseconds() }

// Log returns the faults executed so far.
func (n *Nemesis) Log() []Record {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Record(nil), n.log...)
}

func (n *Nemesis) add(r Record) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.log = append(n.log, r)
	return len(n.log) - 1
}

func (n *Nemesis) end(idx int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if idx >= 0 && idx < len(n.log) && n.log[idx].End == 0 {
		n.log[idx].End = n.now()
	}
}

// endOpen closes every open record of the given kinds.
func (n *Nemesis) endOpen(kinds ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	for i := range n.log {
		for _, k := range kinds {
			if n.log[i].Do == k && n.log[i].End == 0 {
				n.log[i].End = now
			}
		}
	}
}

// Run fires the schedule's events at their times and returns when the last
// one has fired (auto-undos may still be pending; Finish waits for them).
func (n *Nemesis) Run(ctx context.Context, s *Schedule) {
	for _, ev := range s.Events {
		wait := time.Until(n.start.Add(ev.At.D()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		n.Do(ctx, ev)
	}
}

// node resolves a selector.
func (n *Nemesis) node(sel string) int {
	N := n.c.N()
	switch sel {
	case "leader":
		if l, _ := n.c.Leader(); l != 0 {
			return l
		}
		return 1 + n.r.IntN(N)
	case "follower":
		l, _ := n.c.Leader()
		for {
			if f := 1 + n.r.IntN(N); f != l {
				return f
			}
		}
	case "", "random":
		return 1 + n.r.IntN(N)
	}
	var i int
	if _, err := fmt.Sscanf(sel, "n%d", &i); err == nil && i >= 1 && i <= N {
		return i
	}
	return 1 + n.r.IntN(N)
}

func ids(ns []int) string {
	var s []string
	for _, i := range ns {
		s = append(s, cluster.ID(i))
	}
	return strings.Join(s, " ")
}

// Do executes one event.
func (n *Nemesis) Do(ctx context.Context, ev Event) {
	c := n.c
	N := c.N()
	rec := Record{At: n.now(), Do: ev.Do}
	undo := func() {}
	switch ev.Do {
	case "partition":
		perm := n.r.Perm(N)
		for i := range perm {
			perm[i]++
		}
		switch {
		case strings.Contains(ev.Target, "|"):
			// Explicit groups: "n1 n2 n3 | n4 n5".
			var groups [][]int
			for _, g := range strings.Split(ev.Target, "|") {
				var grp []int
				for _, f := range strings.Fields(strings.ReplaceAll(g, ",", " ")) {
					grp = append(grp, n.node(f))
				}
				groups = append(groups, grp)
			}
			c.Mesh.Partition(groups, false)
			rec.Nodes = groups[0]
			var parts []string
			for _, g := range groups {
				parts = append(parts, "["+ids(g)+"]")
			}
			rec.Detail = strings.Join(parts, " | ")
		case ev.Target == "isolate-leader" || ev.Target == "leader-alone":
			l := n.node("leader")
			minority := []int{l}
			if ev.Target == "isolate-leader" {
				for _, p := range perm {
					if p != l {
						minority = append(minority, p)
						break
					}
				}
			}
			var majority []int
			for _, p := range perm {
				if !contains(minority, p) {
					majority = append(majority, p)
				}
			}
			c.Mesh.Partition([][]int{minority, majority}, false)
			rec.Nodes = minority
			rec.Detail = fmt.Sprintf("leader %s cut off: [%s] | [%s]", cluster.ID(l), ids(minority), ids(majority))
		case ev.Target == "ring":
			// Each node talks only to its two ring neighbours: every node
			// sees a majority (itself + 2), but no two see the same one.
			pos := map[int]int{}
			for i, p := range perm {
				pos[p] = i
			}
			c.Mesh.Connect(func(a, b int) bool {
				d := (pos[a] - pos[b] + N) % N
				return d == 1 || d == N-1
			}, false)
			rec.Detail = "majorities ring " + ids(perm)
		case ev.Target == "bridge":
			// {a,b} | c | {d,e} where c talks to both sides.
			bridge := perm[2]
			left, right := perm[:2], perm[3:]
			c.Mesh.Connect(func(a, b int) bool {
				if a == bridge || b == bridge {
					return true
				}
				return contains(left, a) == contains(left, b)
			}, false)
			rec.Nodes = []int{bridge}
			rec.Detail = fmt.Sprintf("bridge: [%s] <- %s -> [%s]", ids(left), cluster.ID(bridge), ids(right))
		default: // halves
			k := 1 + n.r.IntN(N/2)
			c.Mesh.Partition([][]int{perm[:k], perm[k:]}, false)
			rec.Nodes = perm[:k]
			rec.Detail = fmt.Sprintf("[%s] | [%s]", ids(perm[:k]), ids(perm[k:]))
		}
		undo = func() { c.Mesh.Heal(); n.endOpen("partition", "delay") }
	case "heal":
		c.Mesh.Heal()
		n.endOpen("partition", "delay")
		rec.Detail = "all links restored"
	case "delay":
		d := ev.Delay.D()
		if ev.Target == "all" || ev.Target == "" {
			for i := 1; i <= N; i++ {
				for j := 1; j <= N; j++ {
					if i != j {
						c.Mesh.Link(i, j).SetDelay(d)
					}
				}
			}
			rec.Detail = fmt.Sprintf("+%v on every link", d)
		} else {
			t := n.node(ev.Target)
			for j := 1; j <= N; j++ {
				if j != t {
					c.Mesh.Link(t, j).SetDelay(d)
					c.Mesh.Link(j, t).SetDelay(d)
				}
			}
			rec.Nodes = []int{t}
			rec.Detail = fmt.Sprintf("+%v on every link of %s", d, cluster.ID(t))
		}
		undo = func() { c.Mesh.Heal(); n.endOpen("partition", "delay") }
	case "pause":
		t := n.node(ev.Target)
		c.Pause(t)
		rec.Nodes = []int{t}
		rec.Detail = "SIGSTOP " + cluster.ID(t)
		undo = func() { c.Resume(t); n.endOpenNode("pause", t) }
	case "resume":
		for _, t := range c.Paused() {
			c.Resume(t)
			n.endOpenNode("pause", t)
		}
		rec.Detail = "SIGCONT"
	case "kill":
		t := n.node(ev.Target)
		c.Kill(t)
		rec.Nodes = []int{t}
		rec.Detail = "SIGKILL " + cluster.ID(t)
		undo = func() { _ = c.Start(t); n.endOpenNode("kill", t) }
	case "start":
		for i := 1; i <= N; i++ {
			if !c.Running(i) {
				_ = c.Start(i)
				n.endOpenNode("kill", i)
			}
		}
		rec.Detail = "restart crashed nodes"
	case "powerloss":
		var targets []int
		if ev.Target == "all" {
			for i := 1; i <= N; i++ {
				targets = append(targets, i)
			}
		} else {
			targets = []int{n.node(ev.Target)}
		}
		var parts []string
		for _, t := range targets {
			msg, err := c.PowerLoss(t, n.r, true)
			if err != nil {
				msg = err.Error()
			}
			parts = append(parts, cluster.ID(t)+": "+msg)
		}
		for _, t := range targets {
			_ = c.Start(t)
		}
		rec.Nodes = targets
		rec.Detail = "power cut, unsynced writes lost, restarted — " + strings.Join(parts, "; ")
	case "flip-log", "corrupt-snapshot", "snapshot-header", "delete-snapshot":
		t := n.node(ev.Target)
		c.Kill(t)
		var (
			msg string
			err error
		)
		switch ev.Do {
		case "flip-log":
			msg, err = c.FlipLogBit(t, n.r)
		case "corrupt-snapshot":
			msg, err = c.CorruptSnapshot(t, n.r, false)
		case "snapshot-header":
			msg, err = c.CorruptSnapshot(t, n.r, true)
		case "delete-snapshot":
			msg, err = c.DeleteSnapshot(t)
		}
		if err != nil {
			rec.Error = err.Error()
		}
		_ = c.Start(t)
		rec.Nodes = []int{t}
		rec.Detail = fmt.Sprintf("crash %s, %s, restart", cluster.ID(t), msg)
	default:
		rec.Error = "unknown event " + ev.Do
	}
	idx := n.add(rec)
	switch ev.Do {
	case "heal", "resume", "start", "powerloss", "flip-log", "corrupt-snapshot", "snapshot-header", "delete-snapshot":
		n.end(idx)
	}
	if ev.For > 0 {
		n.wg.Add(1)
		go func() {
			defer n.wg.Done()
			select {
			case <-time.After(ev.For.D()):
			case <-ctx.Done():
			}
			undo()
		}()
	}
}

func (n *Nemesis) endOpenNode(kind string, node int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	for i := range n.log {
		if n.log[i].Do == kind && n.log[i].End == 0 && contains(n.log[i].Nodes, node) {
			n.log[i].End = now
		}
	}
}

// Finish waits for pending auto-undos, then heals everything: links back,
// paused nodes resumed, crashed nodes restarted.
func (n *Nemesis) Finish() {
	n.wg.Wait()
	n.c.Mesh.Heal()
	for _, t := range n.c.Paused() {
		n.c.Resume(t)
	}
	for i := 1; i <= n.c.N(); i++ {
		if !n.c.Running(i) {
			_ = n.c.Start(i)
		}
	}
	n.endOpen("partition", "delay", "pause", "kill")
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
