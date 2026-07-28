package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Project struct {
	Path        string
	MachineName string
}

func Resolve(path string) (Project, error) {
	if strings.TrimSpace(path) == "" {
		return Project{}, errors.New("project path must not be empty")
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project path %q: %w", absolute, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return Project{}, fmt.Errorf("inspect project path %q: %w", canonical, err)
	}
	if !info.IsDir() {
		return Project{}, fmt.Errorf("project path %q is not a directory", canonical)
	}
	if _, err := os.Stat(filepath.Join(canonical, ".git")); err != nil {
		return Project{}, fmt.Errorf("project path %q is not a Git repository", canonical)
	}

	return Project{
		Path:        canonical,
		MachineName: machineName(canonical),
	}, nil
}

func machineName(path string) string {
	slug := slugify(filepath.Base(path))
	sum := sha256.Sum256([]byte(path))
	return "isolated-dev-" + slug + "-" + hex.EncodeToString(sum[:8])
}

func slugify(value string) string {
	var result strings.Builder
	previousHyphen := false
	for _, character := range strings.ToLower(value) {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') {
			result.WriteRune(character)
			previousHyphen = false
			continue
		}
		if result.Len() > 0 && !previousHyphen {
			result.WriteByte('-')
			previousHyphen = true
		}
	}
	slug := strings.Trim(result.String(), "-")
	if slug == "" {
		slug = "project"
	}
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	return slug
}
