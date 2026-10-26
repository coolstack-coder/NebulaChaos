package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Build compiles the nebula binary from target/nebulaos at a git ref of
// this repository (a tag such as nebulaos-baseline, a commit, or
// "worktree" for the files as they are on disk) and caches it by commit.
func Build(root, ref string) (string, error) {
	gobin := "go"
	if p, err := exec.LookPath("go"); err == nil {
		gobin = p
	}
	cache := filepath.Join(root, ".cache")
	if ref == "worktree" {
		out := filepath.Join(cache, "bin", "nebula-worktree")
		cmd := exec.Command(gobin, "build", "-o", out, "./cmd/nebula")
		cmd.Dir = filepath.Join(root, "target", "nebulaos")
		if b, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("go build: %v\n%s", err, b)
		}
		return out, nil
	}
	shaB, err := exec.Command("git", "-C", root, "rev-parse", "--verify", ref+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("unknown ref %q", ref)
	}
	sha := strings.TrimSpace(string(shaB))[:12]
	out := filepath.Join(cache, "bin", "nebula-"+sha)
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	src := filepath.Join(cache, "src", sha)
	_ = os.RemoveAll(src)
	if err := os.MkdirAll(src, 0o755); err != nil {
		return "", err
	}
	archive := exec.Command("sh", "-c", fmt.Sprintf("git -C %q archive %s target/nebulaos | tar -x -C %q", root, sha, src))
	if b, err := archive.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git archive: %v\n%s", err, b)
	}
	cmd := exec.Command(gobin, "build", "-o", out, "./cmd/nebula")
	cmd.Dir = filepath.Join(src, "target", "nebulaos")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build at %s: %v\n%s", ref, err, b)
	}
	_ = os.RemoveAll(src)
	return out, nil
}
