package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
)

// The persisted snapshot survives restarts and compaction. NULL (legacy history)
// differs from an empty snapshot: missing files must not be discovered mid-chat.
func conversationInstructions(ctx context.Context, id string, paths []string, projectDir string) (string, error) {
	row, err := db.EntClient.KaguyaConversation.Get(ctx, id)
	if err != nil && !ent.IsNotFound(err) {
		return "", err
	}
	if row != nil {
		if row.DeletedAt != nil {
			return "", ErrConversationNotFound
		}
		if row.AgentInstructions != nil {
			return *row.AgentInstructions, nil
		}
	}
	return loadInstructions(paths, projectDir)
}

// Global files are explicitly trusted paths outside the project root.
func globalInstructions(paths []string) (string, error) {
	return loadInstructions(paths, "")
}

func loadInstructions(paths []string, projectDir string) (string, error) {
	reader := instructionReader{}
	for _, path := range paths {
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			path = filepath.Join(home, path[2:])
		}
		candidates := []string{path}
		if strings.EqualFold(filepath.Base(path), "AGENTS.md") {
			entries, err := os.ReadDir(filepath.Dir(path))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return "", fmt.Errorf("list global instructions %q: %w", path, err)
			}
			candidates = nil
			// ReadDir sorts names; if differently cased files coexist, include all
			// distinct files deterministically, without rewriting the filesystem.
			for _, entry := range entries {
				if strings.EqualFold(entry.Name(), "AGENTS.md") {
					candidates = append(candidates, filepath.Join(filepath.Dir(path), entry.Name()))
				}
			}
		}
		for _, candidate := range candidates {
			f, err := os.OpenFile(candidate, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return "", fmt.Errorf("open global instructions %q: %w", candidate, err)
			}
			if err := reader.append(f, "Global", candidate); err != nil {
				return "", err
			}
		}
	}
	if projectDir != "" {
		// Read through os.Root so project instruction symlinks cannot escape the
		// database-owned workspace, including at the actual open operation.
		root, err := os.OpenRoot(projectDir)
		if err != nil {
			return "", err
		}
		defer root.Close()
		dir, err := root.Open(".")
		if err != nil {
			return "", err
		}
		entries, err := dir.ReadDir(-1)
		closeErr := dir.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		names := instructionNames(entries)
		for _, name := range names {
			f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				return "", fmt.Errorf("open project instructions %q: %w", name, err)
			}
			if err := reader.append(f, "Project", filepath.Join(projectDir, name)); err != nil {
				return "", err
			}
		}
	}
	return reader.out.String(), nil
}

type instructionReader struct {
	out  strings.Builder
	seen []os.FileInfo
}

func instructionNames(entries []os.DirEntry) []string {
	var names []string
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), "AGENTS.md") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func (r *instructionReader) append(f *os.File, scope, path string) (err error) {
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	const maxBytes = 256 * 1024
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("instructions %q must be a regular file", path)
	}
	for _, previous := range r.seen {
		if os.SameFile(previous, info) {
			return nil
		}
	}
	r.seen = append(r.seen, info)
	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes-r.out.Len()+1)))
	if err != nil {
		return err
	}
	if len(data)+r.out.Len() > maxBytes {
		return fmt.Errorf("instructions exceed %d bytes", maxBytes)
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("instructions %q must be UTF-8", path)
	}
	if strings.TrimSpace(string(data)) != "" {
		fmt.Fprintf(&r.out, "\n\n%s instructions from %s:\n%s", scope, path, data)
		if r.out.Len() > maxBytes {
			return fmt.Errorf("instructions exceed %d bytes", maxBytes)
		}
	}
	return nil
}
