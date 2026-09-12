package initialize

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lyonmu/kaguya/internal/config"
)

// SQLCipherKey 在打开数据库前检查密钥，仅为全新部署生成默认密钥。
// 自定义密钥路径必须已存在；已有数据库（包括空文件、WAL）缺失密钥时拒绝生成。
func SQLCipherKey(cfg *config.DatabaseConfig) error {
	if cfg.KeyFile != "" {
		return cfg.ValidateSQLiteKey()
	}
	// 使用副本，初始化成功后再更新配置，失败重试仍遵循默认路径规则。
	resolved := *cfg
	resolved.KeyFile = config.DefaultSQLiteKeyFile
	keyPath, err := resolved.SQLiteKeyPath()
	if err != nil {
		return err
	}
	if _, err := os.Lstat(keyPath); err == nil {
		if err := resolved.ValidateSQLiteKey(); err != nil {
			return err
		}
		cfg.KeyFile = resolved.KeyFile
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check SQLCipher key file: %w", err)
	}
	dbPath, err := resolved.SQLitePath()
	if err != nil {
		return err
	}
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal"} {
		if _, err := os.Lstat(path); err == nil {
			return errors.New("SQLCipher key is missing but database files already exist; restore the original key instead of generating a new one")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check existing SQLCipher database: %w", err)
		}
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate SQLCipher key: %w", err)
	}
	dir := filepath.Dir(keyPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create SQLCipher key directory: %w", err)
	}
	// 先完整写入并同步临时文件，再通过硬链接原子发布；不覆盖并发创建的密钥。
	file, err := os.CreateTemp(dir, ".kaguya-key-*")
	if err != nil {
		return fmt.Errorf("create SQLCipher key: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		return fmt.Errorf("write SQLCipher key: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync SQLCipher key: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close SQLCipher key: %w", err)
	}
	if err := os.Link(file.Name(), keyPath); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("publish SQLCipher key: %w", err)
	}
	if err := resolved.ValidateSQLiteKey(); err != nil {
		return err
	}
	// 保证密钥目录项在数据库开始写入之前同步到磁盘。
	parent, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open SQLCipher key directory: %w", err)
	}
	defer parent.Close()
	if err := parent.Sync(); err != nil {
		return fmt.Errorf("sync SQLCipher key directory: %w", err)
	}
	cfg.KeyFile = resolved.KeyFile
	return nil
}
