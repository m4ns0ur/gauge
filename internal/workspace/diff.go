package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EmptyTree is the well-known tree hash Git uses for "no files". It lets an
// initial-commit repository still produce a diff.
const EmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// DefaultPatchLimit caps the captured diff size. Larger diffs are truncated
// and flagged, so the evaluation contract and reports stay honest.
const DefaultPatchLimit = 8 << 20

// FileStatus is one changed path from `git status`.
type FileStatus struct {
	Index    string `json:"index,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Path     string `json:"path"`
}

// Diff is the candidate's change set captured before evaluation.
type Diff struct {
	Baseline  string       `json:"baseline"`
	Status    string       `json:"status"`
	Patch     []byte       `json:"-"`
	Files     []FileStatus `json:"files"`
	Truncated bool         `json:"truncated"`
}

// HasChanges reports whether the candidate changed the workspace.
func (d Diff) HasChanges() bool { return strings.TrimSpace(d.Status) != "" }

// CaptureDiff records untracked, modified, deleted, and staged changes without
// modifying the candidate worktree or its real index: a temporary index copy is
// used to stage everything for the patch.
func (m *Manager) CaptureDiff(ctx context.Context, repo, scratchDir string) (Diff, error) {
	var d Diff
	baseline, err := m.git(ctx, repo, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err == nil {
		d.Baseline = strings.TrimSpace(baseline)
	} else {
		d.Baseline = EmptyTree
	}

	statusText, err := m.git(ctx, repo, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return d, fmt.Errorf("workspace: capture status: %w", err)
	}
	d.Status = statusText
	d.Files = parseStatus(statusText)

	indexRel, err := m.git(ctx, repo, "rev-parse", "--git-path", "index")
	if err != nil {
		return d, fmt.Errorf("workspace: locate index: %w", err)
	}
	indexPath := strings.TrimSpace(indexRel)
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(repo, indexPath)
	}

	tmpDir, err := os.MkdirTemp(scratchDir, "gauge-diff-*")
	if err != nil {
		return d, fmt.Errorf("workspace: create diff scratch: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	tmpIndex := filepath.Join(tmpDir, "index")
	if err := copyFile(indexPath, tmpIndex); err != nil && !os.IsNotExist(err) {
		return d, fmt.Errorf("workspace: copy index: %w", err)
	}
	env := []string{"GIT_INDEX_FILE=" + tmpIndex}

	if _, err := m.gitWithEnv(ctx, repo, env, "add", "-A"); err != nil {
		return d, fmt.Errorf("workspace: stage changes in temporary index: %w", err)
	}
	base := d.Baseline
	if base == "" {
		base = EmptyTree
	}
	patch, err := m.gitWithEnv(ctx, repo, env,
		"diff", "--cached", "--binary", "--no-color", "--no-ext-diff",
		"--src-prefix=a/", "--dst-prefix=b/", base)
	if err != nil {
		return d, fmt.Errorf("workspace: capture patch: %w", err)
	}
	d.Patch = []byte(patch)
	if len(d.Patch) > DefaultPatchLimit {
		d.Patch = d.Patch[:DefaultPatchLimit]
		d.Truncated = true
	}
	return d, nil
}

// parseStatus parses porcelain v1 output. It intentionally handles the
// human-readable form because the caller already captured it; rename arrows are
// reduced to the destination path.
func parseStatus(status string) []FileStatus {
	var files []FileStatus
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		fs := FileStatus{
			Index:    line[0:1],
			Worktree: line[1:2],
			Path:     strings.TrimSpace(line[3:]),
		}
		if i := strings.Index(fs.Path, " -> "); i >= 0 {
			fs.Path = fs.Path[i+4:]
		}
		files = append(files, fs)
	}
	return files
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
