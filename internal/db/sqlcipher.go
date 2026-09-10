package db

import (
	"database/sql"
	"fmt"
	"strings"
)

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
