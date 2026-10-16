package main

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func isolatedPostgresURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("COCONET_POSTGRES_TEST_URL")
	if raw == "" {
		t.Skip("set COCONET_POSTGRES_TEST_URL for isolated PostgreSQL validation")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("PostgreSQL test configuration must be a postgres URL")
	}
	db, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal("open PostgreSQL test connection failed")
	}
	t.Cleanup(func() { _ = db.Close() })
	schema, err := randomID("coconet_test")
	if err != nil {
		t.Fatal(err)
	}
	identifier := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal("create isolated PostgreSQL schema failed")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error("remove owned PostgreSQL schema failed")
		}
	})
	query := parsed.Query()
	query.Set("search_path", identifier)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func postgresTestStore(t *testing.T, databaseURL string) *store {
	t.Helper()
	storage, err := openPostgresStore(databaseURL)
	if err != nil {
		t.Fatal("open isolated PostgreSQL store failed")
	}
	t.Cleanup(func() { _ = storage.close() })
	return storage
}
