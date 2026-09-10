package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Global instructions are explicitly configured trusted files, outside project os.Root.
// Missing defaults are normal; unreadable or oversized files must not disappear silently.
func globalInstructions(paths []string) (string, error) {
	const maxBytes = 256 * 1024
	var out strings.Builder
	seen := map[string]bool{}
	for _, path := range paths {
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			path = filepath.Join(home, path[2:])
		}
		real, err := filepath.EvalSymlinks(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("resolve global instructions %q: %w", path, err)
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		f, err := os.Open(real)
		if err != nil {
			return "", fmt.Errorf("open global instructions %q: %w", path, err)
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return "", fmt.Errorf("global instructions %q must be a regular file", path)
		}
		data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		if len(data)+out.Len() > maxBytes {
			return "", fmt.Errorf("global instructions exceed %d bytes", maxBytes)
		}
		if !utf8.Valid(data) {
			return "", fmt.Errorf("global instructions %q must be UTF-8", path)
		}
		if strings.TrimSpace(string(data)) != "" {
			fmt.Fprintf(&out, "\n\nGlobal instructions from %s:\n%s", path, data)
			if out.Len() > maxBytes {
				return "", fmt.Errorf("global instructions exceed %d bytes", maxBytes)
			}
		}
	}
	return out.String(), nil
}
