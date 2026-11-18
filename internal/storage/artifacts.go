package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// HashFile computes an artifact record for path.
func HashFile(path string) (Artifact, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Artifact{}, err
	}
	if info.IsDir() {
		return Artifact{}, fmt.Errorf("storage: %s is a directory", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: path, Size: info.Size(), SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// CollectArtifacts hashes the named files inside attemptDir, skipping files an
// adapter did not produce. Returned paths are relative to attemptDir.
func CollectArtifacts(attemptDir string, names ...string) (map[string]Artifact, error) {
	var problems []error
	artifacts := make(map[string]Artifact, len(names))
	for _, name := range names {
		full := filepath.Join(attemptDir, name)
		info, err := os.Stat(full)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			problems = append(problems, fmt.Errorf("inspect artifact %s: %w", name, err))
			continue
		}
		if info.IsDir() {
			problems = append(problems, fmt.Errorf("artifact %s is a directory", name))
			continue
		}
		artifact, err := HashFile(full)
		if err != nil {
			problems = append(problems, fmt.Errorf("hash artifact %s: %w", name, err))
			continue
		}
		artifact.Path = filepath.ToSlash(name)
		artifacts[name] = artifact
	}
	return artifacts, errors.Join(problems...)
}
