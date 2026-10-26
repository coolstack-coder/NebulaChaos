// Package cluster runs a real NebulaOS cluster as separate OS processes,
// wired through the proxy mesh, and gives the nemesis its levers: kill,
// pause, power loss and on-disk corruption.
package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coolstack-coder/nebulachaos/internal/proxy"
)

type Config struct {
	Bin      string   // nebula binary
	Dir      string   // run directory: data and logs go below it
	N        int      // voters
	Env      []string // extra environment, e.g. NEBULA_CHAOS_BUGS=...
	SnapEach int      // --snapshot-every; small values exercise snapshots
	Election time.Duration
	// Port bases. Node i listens on GRPCBase+i and APIBase+i; link i→j is
	// proxied on ProxyBase+10*i+j.
	GRPCBase, APIBase, ProxyBase int
}

func (c *Config) defaults() {
	if c.N == 0 {
		c.N = 5
	}
	if c.SnapEach == 0 {
		c.SnapEach = 64
	}
	if c.Election == 0 {
		c.Election = 450 * time.Millisecond
	}
	if c.GRPCBase == 0 {
		c.GRPCBase, c.APIBase, c.ProxyBase = 17100, 18100, 19000
	}
}

type proc struct {
	cmd    *exec.Cmd
	exited chan struct{}
	paused bool
	killed bool
}

type Cluster struct {
	cfg  Config
	Mesh *proxy.Mesh

	mu    sync.Mutex
	procs map[int]*proc
	// startErr records nodes that refused to start (e.g. corrupt data);
	// refusing is the correct reaction to corruption, so it is reported,
	// not treated as a harness error.
	startErr map[int]string
}

func ID(i int) string { return fmt.Sprintf("n%d", i) }

func (c *Cluster) GRPCAddr(i int) string { return fmt.Sprintf("127.0.0.1:%d", c.cfg.GRPCBase+i) }
func (c *Cluster) APIAddr(i int) string  { return fmt.Sprintf("127.0.0.1:%d", c.cfg.APIBase+i) }
func (c *Cluster) DataDir(i int) string  { return filepath.Join(c.cfg.Dir, "data", ID(i)) }
func (c *Cluster) N() int                { return c.cfg.N }

// New starts the proxy mesh; nodes are started with StartAll.
func New(cfg Config) (*Cluster, error) {
	cfg.defaults()
	c := &Cluster{cfg: cfg, procs: map[int]*proc{}, startErr: map[int]string{}}
	m, err := proxy.NewMesh(cfg.N,
		func(s, d int) int { return cfg.ProxyBase + 10*s + d },
		c.GRPCAddr)
	if err != nil {
		return nil, fmt.Errorf("proxy mesh: %w", err)
	}
	c.Mesh = m
	return c, nil
}

// peers is node i's private view of the cluster: itself directly, every
// other node through the proxy for link i→j.
func (c *Cluster) peers(i int) string {
	var parts []string
	for j := 1; j <= c.cfg.N; j++ {
		addr := c.GRPCAddr(j)
		if j != i {
			addr = fmt.Sprintf("127.0.0.1:%d", c.cfg.ProxyBase+10*i+j)
		}
		parts = append(parts, ID(j)+"="+addr)
	}
	return strings.Join(parts, ",")
}

// Start launches node i (again) with its existing data directory.
func (c *Cluster) Start(i int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.procs[i]; p != nil {
		select {
		case <-p.exited:
		default:
			return nil // already running
		}
	}
	if err := os.MkdirAll(filepath.Join(c.cfg.Dir, "logs"), 0o755); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(c.cfg.Dir, "logs", ID(i)+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	fmt.Fprintf(logf, "\n===== nebulachaos: starting %s at %s =====\n", ID(i), time.Now().Format(time.RFC3339Nano))
	cmd := exec.Command(c.cfg.Bin, "node",
		"--id", ID(i),
		"--grpc", c.GRPCAddr(i), "--advertise-grpc", c.GRPCAddr(i),
		"--api", c.APIAddr(i),
		"--peers", c.peers(i),
		"--data", c.DataDir(i),
		"--runtime", "sim", "--cpu", "4000", "--mem", "8192",
		"--snapshot-every", fmt.Sprint(c.cfg.SnapEach),
		"--election-timeout", c.cfg.Election.String(),
		"--log", "info",
	)
	cmd.Env = append(append(os.Environ(), "NEBULA_CHAOS_LEDGER=1"), c.cfg.Env...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return err
	}
	p := &proc{cmd: cmd, exited: make(chan struct{})}
	c.procs[i] = p
	delete(c.startErr, i)
	go func() {
		err := cmd.Wait()
		logf.Close()
		c.mu.Lock()
		if err != nil && c.procs[i] == p && !p.killed {
			c.startErr[i] = err.Error()
		}
		c.mu.Unlock()
		close(p.exited)
	}()
	return nil
}

func (c *Cluster) StartAll() error {
	for i := 1; i <= c.cfg.N; i++ {
		if err := c.Start(i); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cluster) signal(i int, sig syscall.Signal) {
	c.mu.Lock()
	p := c.procs[i]
	c.mu.Unlock()
	if p == nil || p.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, sig)
}

// Running reports whether node i's process is alive (it may be paused).
func (c *Cluster) Running(i int) bool {
	c.mu.Lock()
	p := c.procs[i]
	c.mu.Unlock()
	if p == nil {
		return false
	}
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// Kill sends SIGKILL and waits for the process to be gone. The page cache
// survives, so nothing written is lost: this is a process crash.
func (c *Cluster) Kill(i int) {
	c.mu.Lock()
	p := c.procs[i]
	c.mu.Unlock()
	if p == nil {
		return
	}
	c.mu.Lock()
	p.killed = true
	c.mu.Unlock()
	c.signal(i, syscall.SIGKILL)
	c.signal(i, syscall.SIGCONT)
	select {
	case <-p.exited:
	case <-time.After(5 * time.Second):
	}
	c.mu.Lock()
	p.paused = false
	c.mu.Unlock()
}

// Pause freezes node i with SIGSTOP. Its sockets stay open and its peers
// see silence, the nastiest kind of "slow": a GC pause or a VM migration.
func (c *Cluster) Pause(i int) {
	c.signal(i, syscall.SIGSTOP)
	c.mu.Lock()
	if p := c.procs[i]; p != nil {
		p.paused = true
	}
	c.mu.Unlock()
}

func (c *Cluster) Resume(i int) {
	c.signal(i, syscall.SIGCONT)
	c.mu.Lock()
	if p := c.procs[i]; p != nil {
		p.paused = false
	}
	c.mu.Unlock()
}

// Paused lists paused nodes.
func (c *Cluster) Paused() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []int
	for i, p := range c.procs {
		if p.paused {
			out = append(out, i)
		}
	}
	sort.Ints(out)
	return out
}

// StartErrors returns nodes whose last start exited with an error.
func (c *Cluster) StartErrors() map[int]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[int]string{}
	for k, v := range c.startErr {
		out[k] = v
	}
	return out
}

func (c *Cluster) Stop() {
	for i := 1; i <= c.cfg.N; i++ {
		c.Kill(i)
	}
	c.Mesh.Close()
}

// RaftStatus is the subset of GET /api/raft the harness uses.
type RaftStatus struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	Term        uint64 `json:"term"`
	Leader      string `json:"leader"`
	CommitIndex uint64 `json:"commit_index"`
	LastIndex   uint64 `json:"last_index"`
	SnapIndex   uint64 `json:"snapshot_index"`
}

var statusClient = &http.Client{Timeout: 300 * time.Millisecond}

func (c *Cluster) Status(i int) (RaftStatus, error) {
	var s RaftStatus
	resp, err := statusClient.Get("http://" + c.APIAddr(i) + "/api/raft")
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&s)
	return s, err
}

// Leader returns the node that claims leadership in the highest term, or 0.
func (c *Cluster) Leader() (int, uint64) {
	best, term := 0, uint64(0)
	for i := 1; i <= c.cfg.N; i++ {
		s, err := c.Status(i)
		if err == nil && s.Role == "leader" && s.Term >= term {
			best, term = i, s.Term
		}
	}
	return best, term
}

// WaitLeader polls until some node is leader.
func (c *Cluster) WaitLeader(ctx context.Context) (int, error) {
	for {
		if l, _ := c.Leader(); l != 0 {
			return l, nil
		}
		select {
		case <-ctx.Done():
			return 0, errors.New("no leader elected")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
