package config

import "testing"

func TestPostgreSQLDSN(t *testing.T) {
	cfg := DatabaseConfig{
		Host:     "2001:db8::1",
		Port:     5432,
		User:     "user@example.com",
		Password: "p@ss/word",
		DBName:   "kaguya db",
		SSLMode:  "require",
	}

	got := cfg.PostgreSQLDSN()
	want := "postgres://user%40example.com:p%40ss%2Fword@[2001:db8::1]:5432/kaguya%20db?sslmode=require"
	if got != want {
		t.Fatalf("PostgreSQLDSN() = %q, want %q", got, want)
	}
}

func TestPostgreSQLDSNDefaults(t *testing.T) {
	cfg := DatabaseConfig{
		Host:   "localhost",
		User:   "postgres",
		DBName: "kaguya",
	}

	got := cfg.PostgreSQLDSN()
	want := "postgres://postgres:@localhost:5432/kaguya?sslmode=disable"
	if got != want {
		t.Fatalf("PostgreSQLDSN() = %q, want %q", got, want)
	}
}

func TestMySQLDSNDefaultsPort(t *testing.T) {
	cfg := DatabaseConfig{
		Host:     "localhost",
		User:     "root",
		Password: "secret",
		DBName:   "kaguya",
	}

	got := cfg.MySQLDSN()
	want := "root:secret@tcp(localhost:3306)/kaguya?charset=utf8mb4&parseTime=true&loc=Local"
	if got != want {
		t.Fatalf("MySQLDSN() = %q, want %q", got, want)
	}
}
