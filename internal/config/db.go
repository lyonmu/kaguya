package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const DefaultSQLiteKeyFile = "~/.kaguya/kaguya.key"

type DatabaseConfig struct {
	Path    string `name:"path" env:"DB_PATH" default:"~/.kaguya/kaguya.db" help:"SQLCipher 数据库文件地址" mapstructure:"path" yaml:"path" json:"path"`
	KeyFile string `name:"key-file" env:"DB_KEY_FILE" help:"SQLCipher 密钥文件（默认 ~/.kaguya/kaguya.key，首次启动自动生成；自定义路径必须已存在）" mapstructure:"key_file" yaml:"key_file" json:"-"`
}

// EnsureSQLiteDatabase 检查 SQLite 数据库文件是否存在，如果不存在则创建
func (c *DatabaseConfig) EnsureSQLiteDatabase() error {
	if _, err := c.sqliteKey(); err != nil {
		return err
	}
	resolvedPath, err := c.SQLitePath()
	if err != nil {
		return err
	}
	c.Path = resolvedPath
	// 新建目录仅允许当前用户访问；不修改已有目录权限。
	dir := filepath.Dir(c.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create sqlite database directory %s: %v", dir, err)
	}

	file, err := os.OpenFile(c.Path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create sqlite database file %s: %w", c.Path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close sqlite database file %s: %w", c.Path, err)
	}
	return nil
}

// SQLiteDSN 返回 SQLCipher DSN，包含密钥，禁止记录到日志或错误中。
// URI key 在 sqlite3_open_v2 内应用，早于驱动执行 WAL 等 PRAGMA。
func (c *DatabaseConfig) SQLiteDSN() (string, error) {
	path, err := c.SQLitePath()
	if err != nil {
		return "", err
	}
	key, err := c.sqliteKey()
	if err != nil {
		return "", err
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := url.Values{}
	query.Set("key", "x'"+key+"'")
	query.Set("_busy_timeout", "5000")
	query.Set("_journal_mode", "WAL")
	query.Set("_foreign_keys", "on")
	query.Set("_synchronous", "FULL")
	dsn.RawQuery = query.Encode()
	return dsn.String(), nil
}

// sqliteKey 仅接受 32 字节随机密钥的十六进制编码，不接受口令或证书。
func (c *DatabaseConfig) sqliteKey() (string, error) {
	if c.KeyFile == "" {
		return "", errors.New("SQLCipher key file is required; configure --db.key-file or DB_KEY_FILE")
	}
	path, err := c.SQLiteKeyPath()
	if err != nil {
		return "", fmt.Errorf("resolve SQLCipher key file: %w", err)
	}
	// 先排除 FIFO／设备，避免打开配置错误的密钥路径时阻塞。
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat SQLCipher key file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("SQLCipher key file must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open SQLCipher key file: %w", err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat SQLCipher key file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("SQLCipher key file must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("SQLCipher key file must not be accessible by group or others; use chmod 600")
	}
	// 最多读取 64 个十六进制字符和 CRLF，再多一个字节用于检测超长输入。
	data, err := io.ReadAll(io.LimitReader(file, 67))
	if err != nil {
		return "", fmt.Errorf("read SQLCipher key file: %w", err)
	}
	key := strings.TrimSuffix(strings.TrimSuffix(string(data), "\r\n"), "\n")
	if len(data) > 66 || len(key) != 64 {
		return "", errors.New("SQLCipher key file must contain exactly 64 hexadecimal characters (32 random bytes), optionally followed by a newline")
	}
	if _, err := hex.DecodeString(key); err != nil {
		return "", errors.New("SQLCipher key file contains invalid hexadecimal encoding")
	}
	return key, nil
}

// ValidateSQLiteKey 检查密钥文件，不向调用方暴露密钥内容。
func (c *DatabaseConfig) ValidateSQLiteKey() error {
	_, err := c.sqliteKey()
	return err
}

// SQLiteKeyPath 解析配置的密钥文件路径，不读取密钥。
func (c *DatabaseConfig) SQLiteKeyPath() (string, error) {
	keyConfig := DatabaseConfig{Path: c.KeyFile}
	return keyConfig.SQLitePath()
}

// SQLitePath 解析数据库文件路径，不创建文件。
func (c *DatabaseConfig) SQLitePath() (string, error) {
	path := c.Path
	if path == "" {
		return "", errors.New("sqlite database path is required")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve sqlite home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		if c.Path == "~" {
			path = home
		}
	} else if strings.HasPrefix(path, "~") {
		return "", errors.New("sqlite database path does not support ~user expansion")
	}
	return filepath.Abs(path)
}
