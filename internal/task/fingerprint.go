package task

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Fingerprint is a content hash of everything that defines the task: the
// declarative spec, the prompt text, the evaluator asset tree, and the pinned
// fixture revision. Two experiments can only be compared task-by-task when
// these fingerprints match.
func (t *Task) Fingerprint() (string, error) {
	spec := *t
	spec.Dir = ""
	spec.Prompt = ""
	spec.FixtureDir = ""
	spec.EvaluatorDir = ""
	encoded, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("fingerprint task %q: %w", t.ID, err)
	}
	assets, err := AssetTreeHash(t.EvaluatorDir)
	if err != nil {
		return "", fmt.Errorf("fingerprint task %q evaluator assets: %w", t.ID, err)
	}

	h := sha256.New()
	writeSection(h, "spec", encoded)
	writeSection(h, "prompt", []byte(t.Prompt))
	writeSection(h, "evaluator", []byte(assets))
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// AssetTreeHash hashes a directory tree deterministically. It is used to pin
// evaluator assets, so changing a hidden test changes the experiment identity.
func AssetTreeHash(root string) (string, error) {
	type entry struct {
		rel  string
		mode fs.FileMode
		size int64
		link string
	}
	var entries []entry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := entry{rel: filepath.ToSlash(rel), mode: info.Mode()}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			e.link = target
		} else if info.Mode().IsRegular() {
			e.size = info.Size()
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	h := sha256.New()
	for _, e := range entries {
		writeSection(h, "path", []byte(e.rel))
		writeSection(h, "mode", []byte(e.mode.String()))
		if e.link != "" {
			writeSection(h, "link", []byte(e.link))
			continue
		}
		writeSection(h, "size", []byte(fmt.Sprintf("%d", e.size)))
		if !e.mode.IsRegular() {
			continue
		}
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(e.rel)))
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeSection(w io.Writer, name string, data []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	_, _ = io.WriteString(w, name)
	_, _ = w.Write([]byte{0})
	_, _ = w.Write(size[:])
	_, _ = w.Write(data)
}
