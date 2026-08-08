package config

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/redis/go-redis/v9"
)

type DatabaseConfig struct {
	Kind     consts.DBKind `enum:"sqlite,mysql" name:"kind" env:"DB_KIND" default:"mysql" help:"数据库类型 [sqlite,mysql]" mapstructure:"kind" yaml:"kind" json:"kind"`
	Path     string        `name:"path" env:"DB_PATH" default:"./data/kaguya.sqlite" help:"sqlite数据库文件地址" mapstructure:"path" yaml:"path" json:"path"`
	Host     string        `name:"host" env:"MYSQL_HOST" default:"127.0.0.1" help:"mysql数据库主机" mapstructure:"host" yaml:"host" json:"host"`
	Port     int           `name:"port" env:"MYSQL_PORT" default:"3306" help:"mysql数据库端口" mapstructure:"port" yaml:"port" json:"port"`
	User     string        `name:"user" env:"MYSQL_USER" default:"root" help:"mysql数据库用户" mapstructure:"user" yaml:"user" json:"user"`
	Password string        `name:"password" env:"MYSQL_PASSWORD" default:"root" help:"mysql数据库密码" mapstructure:"password" yaml:"password" json:"password"`
	DBName   string        `name:"db_name" env:"MYSQL_DB_NAME" default:"kaguya" help:"mysql数据库名称" mapstructure:"db_name" yaml:"db_name" json:"db_name"`
}

// EnsureDatabase 检查数据库是否存在，如果不存在则创建
func (c *DatabaseConfig) EnsureMySQLDatabase() error {
	// 连接到 MySQL 服务器（不指定数据库名）
	dsnWithoutDB := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=utf8mb4&parseTime=true&loc=Local",
		c.User, c.Password, c.Host, c.Port)

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
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true&loc=Local", c.User, c.Password, c.Host, c.Port, c.DBName)
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
