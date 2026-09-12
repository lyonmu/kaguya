package db

import (
	"context"
	"database/sql"
	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/mattn/go-sqlite3"
)

var EntClient *ent.Client

// SQL statement arguments can contain provider keys, MCP credentials and chats.
// Application debug mode must never enable Ent SQL argument logging.

// InitSQLite 初始化 SQLCipher 加密数据库，保持 Ent 的 SQLite dialect。
func InitSQLite(c *config.DatabaseConfig) (*ent.Client, error) {
	// 在打开业务文件前检查引擎，避免普通 SQLite 忽略 key 后写入明文。
	if err := requireSQLCipher(); err != nil {
		return nil, err
	}
	dsn, err := c.SQLiteDSN()
	if err != nil {
		return nil, err
	}
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 同时只能有一个写事务；限制应用连接数以串行化进程内写入。
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	var journalMode string
	if err := conn.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		_ = conn.Close()
		// 不传播可能包含敏感 DSN 的底层错误。
		return nil, fmt.Errorf("open SQLCipher database failed: verify the key, encrypted SQLCipher 4 format, file permissions and lock availability; plaintext databases require explicit migration")
	}
	if journalMode != "wal" {
		_ = conn.Close()
		return nil, fmt.Errorf("expected sqlite WAL journal mode, got %q", journalMode)
	}
	// 强制读取 schema 来验证密钥；设置 key 成功不代表能够解密数据库。
	var tables int
	if err := conn.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&tables); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot read SQLCipher schema: incorrect key or damaged encrypted database")
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, conn)))
	if createErr := client.Schema.Create(context.Background(), migrate.WithForeignKeys(false)); createErr != nil {
		_ = client.Close()
		return nil, createErr
	}
	return client, nil
}
