// Package proxy puts a userspace TCP proxy on every directed link between
// cluster nodes, so the nemesis can cut, slow down or heal any single link
// without root, iptables or containers.
//
// Node i is started with a private peer map in which node j's address is
// the proxy for link i→j. A gRPC call and its reply travel on the
// connection the caller opened, so link i→j carries i's requests to j and
// j's answers to them; j's own requests to i use link j→i. Cutting one
// direction therefore gives a genuinely asymmetric partition.
package proxy

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// Link forwards connections from one node to another.
type Link struct {
	Src, Dst int
	ln       net.Listener
	target   string

	mu    sync.Mutex
	cond  *sync.Cond
	cut   bool
	delay time.Duration
	conns map[net.Conn]struct{}
	done  bool
}

func newLink(src, dst int, listen, target string) (*Link, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	l := &Link{Src: src, Dst: dst, ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	l.cond = sync.NewCond(&l.mu)
	go l.accept()
	return l, nil
}

func (l *Link) accept() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			return
		}
		go l.serve(c)
	}
}

// waitOpen blocks while the link is cut. It reports false once the link is
// closed for good.
func (l *Link) waitOpen() (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.cut && !l.done {
		l.cond.Wait()
	}
	return l.delay, !l.done
}

func (l *Link) track(c net.Conn, add bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if add {
		l.conns[c] = struct{}{}
	} else {
		delete(l.conns, c)
	}
}

func (l *Link) serve(in net.Conn) {
	// A connection attempted during a partition hangs, like a SYN into a
	// black hole, instead of being refused.
	if _, ok := l.waitOpen(); !ok {
		in.Close()
		return
	}
	out, err := net.DialTimeout("tcp", l.target, time.Second)
	if err != nil {
		in.Close()
		return
	}
	l.track(in, true)
	l.track(out, true)
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			in.Close()
			out.Close()
			l.track(in, false)
			l.track(out, false)
		})
	}
	go l.pump(out, in, closeBoth)
	l.pump(in, out, closeBoth)
}

// pump copies src to dst one chunk at a time. While the link is cut it
// holds the chunk it has and stops reading, so bytes pile up in kernel
// buffers and the sender's RPCs time out: a blackhole, not a reset.
func (l *Link) pump(dst, src net.Conn, closeBoth func()) {
	defer closeBoth()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			delay, ok := l.waitOpen()
			if !ok {
				return
			}
			if delay > 0 {
				time.Sleep(delay)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// SetCut blackholes (true) or restores (false) the link. With reset, open
// connections are also torn down, so queued messages are dropped instead of
// being delivered late when the link heals.
func (l *Link) SetCut(cut, reset bool) {
	l.mu.Lock()
	l.cut = cut
	var victims []net.Conn
	if reset {
		for c := range l.conns {
			victims = append(victims, c)
		}
	}
	l.cond.Broadcast()
	l.mu.Unlock()
	for _, c := range victims {
		c.Close()
	}
}

// SetDelay adds latency to every chunk forwarded on the link.
func (l *Link) SetDelay(d time.Duration) {
	l.mu.Lock()
	l.delay = d
	l.mu.Unlock()
}

func (l *Link) Close() {
	l.mu.Lock()
	l.done = true
	l.cond.Broadcast()
	var cs []net.Conn
	for c := range l.conns {
		cs = append(cs, c)
	}
	l.mu.Unlock()
	l.ln.Close()
	for _, c := range cs {
		c.Close()
	}
}

// Mesh is the full set of directed links between n nodes, numbered 1..n.
type Mesh struct {
	N     int
	links map[[2]int]*Link
}

// PortFunc gives the proxy listen port for link src→dst.
type PortFunc func(src, dst int) int

// NewMesh starts a proxy for every ordered pair. target(j) is node j's real
// gRPC address.
func NewMesh(n int, port PortFunc, target func(int) string) (*Mesh, error) {
	m := &Mesh{N: n, links: map[[2]int]*Link{}}
	for i := 1; i <= n; i++ {
		for j := 1; j <= n; j++ {
			if i == j {
				continue
			}
			l, err := newLink(i, j, fmt.Sprintf("127.0.0.1:%d", port(i, j)), target(j))
			if err != nil {
				m.Close()
				return nil, err
			}
			m.links[[2]int{i, j}] = l
		}
	}
	return m, nil
}

func (m *Mesh) Link(src, dst int) *Link { return m.links[[2]int{src, dst}] }

// Partition cuts every link between nodes in different groups, in both
// directions, and heals every link inside a group. Nodes not listed are
// isolated from everyone.
func (m *Mesh) Partition(groups [][]int, reset bool) {
	group := map[int]int{}
	for g, ns := range groups {
		for _, n := range ns {
			group[n] = g + 1
		}
	}
	for k, l := range m.links {
		same := group[k[0]] != 0 && group[k[0]] == group[k[1]]
		l.SetCut(!same, reset && !same)
	}
}

// Connect keeps exactly the links for which ok(src, dst) holds and cuts the
// rest. It expresses partitions that are not clean groups, like a ring in
// which every node sees a different majority.
func (m *Mesh) Connect(ok func(src, dst int) bool, reset bool) {
	for k, l := range m.links {
		cut := !ok(k[0], k[1])
		l.SetCut(cut, reset && cut)
	}
}

// Heal restores every link and removes added latency.
func (m *Mesh) Heal() {
	for _, l := range m.links {
		l.SetCut(false, false)
		l.SetDelay(0)
	}
}

func (m *Mesh) Close() {
	for _, l := range m.links {
		l.Close()
	}
}
