package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrDenied = errors.New("path denied")

type deniedError struct {
	reason string
	path   string
}

func (e *deniedError) Error() string {
	return fmt.Sprintf("%s: %s", e.reason, e.path)
}

func (e *deniedError) Unwrap() error {
	return ErrDenied
}

func (s *SafeFS) resolve(userPath string) (absPath string, relPath string, err error) {
	p := strings.TrimSpace(userPath)
	if p == "" {
		p = "."
	}

	if filepath.IsAbs(p) {
		return "", "", &deniedError{reason: "absolute paths are not allowed", path: userPath}
	}

	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", "", &deniedError{reason: "path escapes workspace root", path: userPath}
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
		return "", "", &deniedError{reason: "path escapes workspace root", path: userPath}
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
			return &deniedError{reason: "hidden path is not allowed", path: part}
		}

		if _, ok := s.cfg.DenyDirs[part]; ok {
			return &deniedError{reason: "directory is denied", path: part}
		}

		if _, ok := s.cfg.DenyFileNames[part]; ok {
			return &deniedError{reason: "file is denied", path: part}
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
