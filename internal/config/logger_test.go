package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogFilePathHomeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for path, want := range map[string]string{
		"~/.kaguya/logs":  filepath.Join(home, ".kaguya", "logs"),
		"~":               home,
		"/var/log/kaguya": "/var/log/kaguya",
	} {
		cfg := LogConfig{FileEnabled: true, FilePath: path}
		resolved, err := cfg.resolveFilePath()
		if err != nil {
			t.Fatalf("resolve %q: %v", path, err)
		}
		if resolved != want {
			t.Fatalf("resolve %q = %q, want %q", path, resolved, want)
		}
	}
}

func TestLogFilePathInvalid(t *testing.T) {
	cfg := LogConfig{FileEnabled: true, FilePath: "~other/logs"}
	if _, err := cfg.NewLogger(); err == nil {
		t.Fatal("expected ~user expansion error")
	}
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	cfg.FilePath = "~/.kaguya/logs"
	if _, err := cfg.NewLogger(); err == nil {
		t.Fatal("expected missing home error")
	}
}

func TestNewLoggerCreatesExpandedDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := LogConfig{Module: "kaguya", Level: InfoLevel, Format: JSONFormat, FileEnabled: true, FilePath: "~/.kaguya/logs"}
	logger, err := cfg.NewLogger()
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("startup")
	_ = logger.Sync()
	logDir := filepath.Join(home, ".kaguya", "logs", "kaguya")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("read expanded log directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected a log file in %s", logDir)
	}
}
