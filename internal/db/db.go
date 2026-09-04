package db

import (
	"context"
	"database/sql"

	"entgo.io/ent/dialect"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	"github.com/redis/go-redis/v9"
	moderncsqlite "modernc.org/sqlite"
)

var (
	EntClient *ent.Client
	RedisCli  redis.UniversalClient
)

func init() {
	// ent 的 dialect.SQLite 使用驱动名 "sqlite3"，而 modernc.org/sqlite 注册的是 "sqlite"
	sql.Register("sqlite3", &moderncsqlite.Driver{})
}

// InitMySQL 初始化 MySQL 数据库客户端
func InitMySQL(c *config.DatabaseConfig, debug bool) (*ent.Client, error) {
	options := make([]ent.Option, 0)
	if debug {
		options = append(options, ent.Debug())
	}
	client, openErr := ent.Open(dialect.MySQL, c.MySQLDSN(), options...)
	if openErr != nil {
		return nil, openErr
	}
	if createErr := client.Schema.Create(context.Background(), migrate.WithForeignKeys(false)); createErr != nil {
		return nil, createErr
	}
	return client, nil
}

// InitPostgreSQL 初始化 PostgreSQL 数据库客户端
func InitPostgreSQL(c *config.DatabaseConfig, debug bool) (*ent.Client, error) {
	options := make([]ent.Option, 0)
	if debug {
		options = append(options, ent.Debug())
	}
	client, openErr := ent.Open(dialect.Postgres, c.PostgreSQLDSN(), options...)
	if openErr != nil {
		return nil, openErr
	}
	if createErr := client.Schema.Create(context.Background(), migrate.WithForeignKeys(false)); createErr != nil {
		_ = client.Close()
		return nil, createErr
	}
	return client, nil
}

// InitSQLite 初始化 SQLite 数据库客户端
func InitSQLite(c *config.DatabaseConfig, debug bool) (*ent.Client, error) {
	options := make([]ent.Option, 0)
	if debug {
		options = append(options, ent.Debug())
	}
	client, openErr := ent.Open(dialect.SQLite, c.SQLiteDSN(), options...)
	if openErr != nil {
		return nil, openErr
	}
	if createErr := client.Schema.Create(context.Background(), migrate.WithForeignKeys(false)); createErr != nil {
		return nil, createErr
	}
	return client, nil
}
