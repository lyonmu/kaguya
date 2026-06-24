package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (s *SafeFS) resolve(userPath string) (absPath string, relPath string, err error) {
	p := strings.TrimSpace(userPath)
	if p == "" {
		p = "."
	}

	if filepath.IsAbs(p) {
		return "", "", errors.New("absolute paths are not allowed")
	}

	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("path escapes workspace root")
	}

	joined := filepath.Join(s.root, clean)

	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", "", err
	}

	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", err
	}

	rel, err := filepath.Rel(s.root, real)
	if err != nil {
		return "", "", err
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", "", errors.New("path escapes workspace root")
	}

	if err := s.checkDenied(rel); err != nil {
		return "", "", err
	}

	return real, filepath.ToSlash(rel), nil
}

func (s *SafeFS) checkDenied(rel string) error {
	parts := strings.Split(filepath.ToSlash(rel), "/")

	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}

		if !s.cfg.AllowHidden && strings.HasPrefix(part, ".") {
			return fmt.Errorf("hidden path is not allowed: %s", part)
		}

		if _, ok := s.cfg.DenyDirs[part]; ok {
			return fmt.Errorf("directory is denied: %s", part)
		}

		if _, ok := s.cfg.DenyFileNames[part]; ok {
			return fmt.Errorf("file is denied: %s", part)
		}
	}

	return nil
}

func defaultDenyFileNames() map[string]struct{} {
	names := []string{
		".env",
		".env.local",
		".env.production",
		".npmrc",
		".pypirc",
		".netrc",
		"id_rsa",
		"id_ed25519",
		"known_hosts",
	}

	m := make(map[string]struct{}, len(names))
	for _, name := range names {
		m[name] = struct{}{}
	}
	return m
}

func defaultDenyDirs() map[string]struct{} {
	dirs := []string{
		".git",
		"node_modules",
		"vendor",
		"target",
		"dist",
		"build",
		".idea",
		".vscode",
	}

	m := make(map[string]struct{}, len(dirs))
	for _, name := range dirs {
		m[name] = struct{}{}
	}
	return m
}
