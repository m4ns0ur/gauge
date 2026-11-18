// Package workspace prepares and inspects the per-attempt candidate workspace.
//
// Every attempt gets a fresh Git clone of the task's pinned fixture revision.
// The clone has no remote, so the candidate cannot fetch from or share objects
// with the fixture repository, and no two attempts share mutable Git state.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/m4ns0ur/gauge/internal/proc"
)

// DefaultGitTimeout bounds individual Git operations.
const DefaultGitTimeout = 2 * time.Minute

// Manager materializes workspaces from pinned fixtures.
type Manager struct {
	// WorkRoot is the configured root for attempt workspaces. It is recorded
	// for retention checks.
	WorkRoot string
	// GitPath overrides the git executable (empty means "git" from PATH).
	GitPath string
}

// PrepareRequest describes one workspace to create.
type PrepareRequest struct {
	FixtureDir string
	Revision   string
	Dest       string
}

// Info records the materialized workspace state.
type Info struct {
	Path       string `json:"path"`
	Revision   string `json:"revision"`
	Fixture    string `json:"fixture"`
	GitVersion string `json:"git_version,omitempty"`
}

// Prepare clones the fixture at revision into dest and returns a clean
// workspace. Dest must not exist or must be empty.
func (m *Manager) Prepare(ctx context.Context, req PrepareRequest) (Info, error) {
	if req.FixtureDir == "" || req.Revision == "" || req.Dest == "" {
		return Info{}, fmt.Errorf("workspace: fixture, revision, and destination are required")
	}
	dest, err := filepath.Abs(req.Dest)
	if err != nil {
		return Info{}, fmt.Errorf("workspace: resolve destination: %w", err)
	}
	if m.WorkRoot != "" {
		root, err := filepath.Abs(m.WorkRoot)
		if err != nil {
			return Info{}, fmt.Errorf("workspace: resolve work root: %w", err)
		}
		if !isWithin(root, dest) {
			return Info{}, fmt.Errorf("workspace: destination %q is outside the work root %q", dest, root)
		}
	}
	if err := ensureEmptyDir(dest); err != nil {
		return Info{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Info{}, fmt.Errorf("workspace: create parent directory: %w", err)
	}

	// --no-local forces a real copy instead of hardlinks, so the workspace
	// never shares object files with the fixture.
	cloneArgs := []string{
		"-c", "protocol.file.allow=always",
		"clone", "--quiet", "--no-local", "--no-checkout",
		"--", req.FixtureDir, dest,
	}
	if _, err := m.git(ctx, "", cloneArgs...); err != nil {
		_ = os.RemoveAll(dest)
		return Info{}, fmt.Errorf("workspace: clone fixture %s: %w", req.FixtureDir, err)
	}

	cleanup := func(err error) (Info, error) {
		_ = os.RemoveAll(dest)
		return Info{}, err
	}

	if _, err := m.git(ctx, dest, "checkout", "--quiet", "--detach", req.Revision); err != nil {
		return cleanup(fmt.Errorf("workspace: checkout %s: %w", req.Revision, err))
	}
	head, err := m.git(ctx, dest, "rev-parse", "HEAD")
	if err != nil {
		return cleanup(fmt.Errorf("workspace: resolve HEAD: %w", err))
	}
	head = strings.TrimSpace(head)
	if head != req.Revision {
		return cleanup(fmt.Errorf("workspace: HEAD is %s, want pinned revision %s", head, req.Revision))
	}
	// No remote: the candidate cannot fetch from or push to the fixture.
	if _, err := m.git(ctx, dest, "remote", "remove", "origin"); err != nil {
		return cleanup(fmt.Errorf("workspace: remove origin remote: %w", err))
	}
	for _, kv := range [][2]string{
		{"user.name", "Gauge Benchmark"},
		{"user.email", "gauge@example.invalid"},
		{"commit.gpgsign", "false"},
		{"tag.gpgsign", "false"},
	} {
		if _, err := m.git(ctx, dest, "config", kv[0], kv[1]); err != nil {
			return cleanup(fmt.Errorf("workspace: configure %s: %w", kv[0], err))
		}
	}

	status, err := m.git(ctx, dest, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return cleanup(fmt.Errorf("workspace: initial status: %w", err))
	}
	if strings.TrimSpace(status) != "" {
		// A dirty fixture checkout is not a clean experimental condition.
		return cleanup(fmt.Errorf("workspace: fixture checkout is not clean:\n%s", status))
	}

	gitVersion := ""
	if out, err := m.git(ctx, dest, "version"); err == nil {
		gitVersion = strings.TrimSpace(out)
	}

	return Info{
		Path:       dest,
		Revision:   head,
		Fixture:    req.FixtureDir,
		GitVersion: gitVersion,
	}, nil
}

func ensureEmptyDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("workspace: inspect %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace: %s exists and is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("workspace: read %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("workspace: %s is not empty; refusing to reuse a workspace", dir)
	}
	return nil
}

func isWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// git runs a Git command with an isolated configuration so a developer's
// global aliases, hooks, signing, or filters cannot change benchmark behavior.
func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	return m.gitWithEnv(ctx, dir, nil, args...)
}

func (m *Manager) gitWithEnv(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultGitTimeout)
	defer cancel()
	name := m.GitPath
	if name == "" {
		name = "git"
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	env := gitEnv()
	env = append(env, extraEnv...)
	cmd.Env = env
	proc.Configure(cmd)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		return text, fmt.Errorf("%s (git %s)", strings.TrimSpace(text), strings.Join(args, " "))
	}
	return text, nil
}

func gitEnv() []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+4)
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_GLOBAL",
			"GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM":
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
	)
	return env
}

// Remove deletes only the workspace directory inside attemptDir, leaving logs,
// diffs, events, and evaluation results intact.
func Remove(attemptDir string) error {
	ws := filepath.Join(attemptDir, "workspace")
	if err := removeUnder(attemptDir, ws); err != nil {
		return err
	}
	return nil
}

func removeUnder(base, target string) error {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return err
	}
	if rel != "workspace" {
		return fmt.Errorf("workspace: refusing to remove %q outside %q", absTarget, absBase)
	}
	if err := os.RemoveAll(absTarget); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("workspace: remove %s: %w", absTarget, err)
	}
	return nil
}
