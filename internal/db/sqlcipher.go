package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// RequireSQLCipher 确认当前链接的是 SQLCipher 4 引擎，供离线迁移等直接打开数据库的路径复用。
func RequireSQLCipher() error {
	return requireSQLCipher()
}

func requireSQLCipher() error {
	probe, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return fmt.Errorf("SQLCipher driver is unavailable; build with make build")
	}
	defer probe.Close()
	var version string
	if err := probe.QueryRow("PRAGMA cipher_version").Scan(&version); err != nil || !strings.HasPrefix(version, "4.") {
		return fmt.Errorf("SQLCipher 4 is required; ordinary SQLite and CGO-disabled builds are not supported; use make build")
	}
	return nil
}
