package config

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/lib/pq"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/redis/go-redis/v9"
)

const DefaultSQLiteKeyFile = "~/.kaguya/kaguya.key"

type DatabaseConfig struct {
	Kind     consts.DBKind `enum:"sqlite,mysql,postgresql,postgres" name:"kind" env:"DB_KIND" default:"sqlite" help:"数据库类型（当前仅启用 sqlite，其他类型保留但禁用）" mapstructure:"kind" yaml:"kind" json:"kind"`
	Path     string        `name:"path" env:"DB_PATH" default:"~/.kaguya/kaguya.db" help:"sqlite数据库文件地址" mapstructure:"path" yaml:"path" json:"path"`
	KeyFile  string        `name:"key-file" env:"DB_KEY_FILE" help:"SQLCipher 密钥文件（默认 ~/.kaguya/kaguya.key，首次启动自动生成；自定义路径必须已存在）" mapstructure:"key_file" yaml:"key_file" json:"-"`
	Host     string        `name:"host" env:"DB_HOST,MYSQL_HOST,POSTGRES_HOST,PGHOST" default:"127.0.0.1" help:"数据库主机" mapstructure:"host" yaml:"host" json:"host"`
	Port     int           `name:"port" env:"DB_PORT,MYSQL_PORT,POSTGRES_PORT,PGPORT" default:"0" help:"数据库端口（0 表示 MySQL 使用 3306、PostgreSQL 使用 5432）" mapstructure:"port" yaml:"port" json:"port"`
	User     string        `name:"user" env:"DB_USER,MYSQL_USER,POSTGRES_USER,PGUSER" default:"root" help:"数据库用户" mapstructure:"user" yaml:"user" json:"user"`
	Password string        `name:"password" env:"DB_PASSWORD,MYSQL_PASSWORD,POSTGRES_PASSWORD,PGPASSWORD" default:"root" help:"数据库密码" mapstructure:"password" yaml:"password" json:"password"`
	DBName   string        `name:"db_name" env:"DB_NAME,MYSQL_DB_NAME,POSTGRES_DB,PGDATABASE" default:"kaguya" help:"数据库名称" mapstructure:"db_name" yaml:"db_name" json:"db_name"`
	SSLMode  string        `name:"ssl_mode" env:"DB_SSL_MODE,POSTGRES_SSL_MODE,PGSSLMODE" default:"disable" help:"PostgreSQL SSL 模式" mapstructure:"ssl_mode" yaml:"ssl_mode" json:"ssl_mode"`
}

// EnsureDatabase 检查数据库是否存在，如果不存在则创建
func (c *DatabaseConfig) EnsureMySQLDatabase() error {
	// 连接到 MySQL 服务器（不指定数据库名）
	dsnWithoutDB := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=utf8mb4&parseTime=true&loc=Local",
		c.User, c.Password, c.Host, c.databasePort(3306))

	db, err := sql.Open("mysql", dsnWithoutDB)
	if err != nil {
		return fmt.Errorf("failed to connect to mysql server: %v", err)
	}
	defer db.Close()

	// 检查数据库是否存在
	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME = ?)"
	err = db.QueryRow(query, c.DBName).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check database existence: %v", err)
	}

	// 如果数据库不存在，则创建
	if !exists {
		createSQL := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", c.DBName)
		_, err = db.Exec(createSQL)
		if err != nil {
			return fmt.Errorf("failed to create database %s: %v", c.DBName, err)
		}
	}

	return nil
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

func (c *DatabaseConfig) MySQLDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true&loc=Local", c.User, c.Password, c.Host, c.databasePort(3306), c.DBName)
}

// EnsurePostgreSQLDatabase 检查 PostgreSQL 数据库是否存在，如果不存在则创建。
func (c *DatabaseConfig) EnsurePostgreSQLDatabase() error {
	db, err := sql.Open("postgres", c.PostgreSQLDSN())
	if err != nil {
		return fmt.Errorf("failed to open postgresql database: %w", err)
	}
	err = db.Ping()
	if closeErr := db.Close(); closeErr != nil && err == nil {
		return fmt.Errorf("failed to close postgresql database: %w", closeErr)
	}
	if err == nil {
		return nil
	}

	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr.Code != "3D000" {
		return fmt.Errorf("failed to connect to postgresql database %s: %w", c.DBName, err)
	}

	maintenanceDB, openErr := sql.Open("postgres", c.postgreSQLDSN("postgres"))
	if openErr != nil {
		return fmt.Errorf("failed to open postgresql server connection: %w", openErr)
	}
	defer maintenanceDB.Close()
	if pingErr := maintenanceDB.Ping(); pingErr != nil {
		return fmt.Errorf("failed to connect to postgresql server: %w", pingErr)
	}

	_, createErr := maintenanceDB.Exec("CREATE DATABASE " + pq.QuoteIdentifier(c.DBName))
	if createErr != nil {
		if errors.As(createErr, &pqErr) && pqErr.Code == "42P04" {
			return nil
		}
		return fmt.Errorf("failed to create postgresql database %s: %w", c.DBName, createErr)
	}
	return nil
}

// PostgreSQLDSN 返回 PostgreSQL 数据库的数据源名称（DSN）。
func (c *DatabaseConfig) PostgreSQLDSN() string {
	return c.postgreSQLDSN(c.DBName)
}

func (c *DatabaseConfig) postgreSQLDSN(database string) string {
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(c.databasePort(5432))),
		Path:   "/" + database,
	}
	query := dsn.Query()
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	query.Set("sslmode", sslMode)
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func (c *DatabaseConfig) databasePort(defaultPort int) int {
	if c.Port == 0 {
		return defaultPort
	}
	return c.Port
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

type RedisConfig struct {
	Host     []string `name:"host" env:"REDIS_HOST" default:"127.0.0.1:6379" help:"redis服务器地址" mapstructure:"host" yaml:"host" json:"host"`
	Password string   `name:"password" env:"REDIS_PASSWORD" default:"root" help:"redis服务器密码" mapstructure:"password" yaml:"password" json:"password"`
	DB       int      `name:"db" env:"REDIS_DB" default:"1" help:"redis数据库" mapstructure:"db" yaml:"db" json:"db"`
}

func (c *RedisConfig) Client(name string) redis.UniversalClient {
	options := &redis.UniversalOptions{
		Addrs:                 c.Host,
		Password:              c.Password,
		DB:                    c.DB,
		ClientName:            name,
		ContextTimeoutEnabled: true,
		PoolFIFO:              false,
	}
	return redis.NewUniversalClient(options)
}
