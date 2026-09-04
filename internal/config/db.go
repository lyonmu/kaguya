package config

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/lib/pq"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/redis/go-redis/v9"
)

type DatabaseConfig struct {
	Kind     consts.DBKind `enum:"sqlite,mysql,postgresql,postgres" name:"kind" env:"DB_KIND" default:"mysql" help:"数据库类型 [sqlite,mysql,postgresql,postgres]" mapstructure:"kind" yaml:"kind" json:"kind"`
	Path     string        `name:"path" env:"DB_PATH" default:"./data/kaguya.sqlite" help:"sqlite数据库文件地址" mapstructure:"path" yaml:"path" json:"path"`
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
	// 确保数据库文件所在目录存在
	dir := filepath.Dir(c.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create sqlite database directory %s: %v", dir, err)
	}

	// 如果数据库文件不存在，则创建
	if _, err := os.Stat(c.Path); os.IsNotExist(err) {
		file, err := os.Create(c.Path)
		if err != nil {
			return fmt.Errorf("failed to create sqlite database file %s: %v", c.Path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("failed to close sqlite database file %s: %v", c.Path, err)
		}
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

// SQLiteDSN 返回 SQLite 数据库的数据源名称（DSN）
func (c *DatabaseConfig) SQLiteDSN() string {
	return fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", c.Path)
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
