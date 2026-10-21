package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// openTemp opens a fresh database in a per-test temp dir.
func openTemp(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hive_test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestMigrateToLatest(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()

	var version int
	if err := d.W.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}

	// Spot-check tables created across several migrations.
	for _, table := range []string{"folders", "feeds", "articles", "enclosures", "settings", "sync_queue", "tags", "article_tags", "rules", "newsletter_sources", "highlights", "articles_fts"} {
		var name string
		err := d.R.QueryRowContext(ctx,
			"SELECT name FROM sqlite_master WHERE name = ?", table).Scan(&name)
		if err != nil {
			t.Errorf("expected table %q to exist: %v", table, err)
		}
	}

	// Spot-check a late-migration column exists.
	var dflt any
	if err := d.R.QueryRowContext(ctx,
		"SELECT refresh_interval_min FROM feeds LIMIT 1").Scan(&dflt); err != nil {
		// No rows is fine; a missing column would be a different error.
		if err.Error() != "sql: no rows in result set" {
			t.Errorf("refresh_interval_min column missing: %v", err)
		}
	}
}

func TestUnicodeLower(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()

	// Matches Go's strings.ToLower, which the rule-matching code uses — that
	// consistency is the whole point of registering unicode_lower.
	var got string
	if err := d.R.QueryRowContext(ctx, "SELECT unicode_lower(?)", "ÀÉÇ Straße ДВА").Scan(&got); err != nil {
		t.Fatalf("unicode_lower: %v", err)
	}
	if want := "àéç straße два"; got != want {
		t.Errorf("unicode_lower = %q, want %q", got, want)
	}

	// NULL folds to NULL.
	var n any
	if err := d.R.QueryRowContext(ctx, "SELECT unicode_lower(NULL)").Scan(&n); err != nil {
		t.Fatalf("unicode_lower(NULL): %v", err)
	}
	if n != nil {
		t.Errorf("unicode_lower(NULL) = %v, want nil", n)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")
	d1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = d1.Close()

	// Reopening an already-migrated database must be a no-op, not re-apply.
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d2.Close()

	var version int
	if err := d2.W.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("after reopen user_version = %d, want %d", version, len(migrations))
	}
}

// TestMigrateRejectsNewerDatabase pins the newer-build guard: a database whose
// user_version is ahead of this build's migrations must fail to open — not be
// silently adopted and written with an old schema — and must be left byte-for-
// byte intact so a compatible build can still open it. Ported from papr's
// v0.16.1 "preserve existing databases on startup" fix (#141).
func TestMigrateRejectsNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")

	// Build a minimal database at a schema version beyond this build's set:
	// one table plus a row, exactly what a future migration set would leave.
	d1, err := Open(path)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	ctx := context.Background()
	if _, err := d1.W.ExecContext(ctx, "CREATE TABLE future(col TEXT)"); err != nil {
		t.Fatalf("create future table: %v", err)
	}
	if _, err := d1.W.ExecContext(ctx, "INSERT INTO future(col) VALUES ('keep')"); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	if _, err := d1.W.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+3)); err != nil {
		t.Fatalf("set future user_version: %v", err)
	}
	_ = d1.Close()

	// Reopening must fail, and the marker row must survive untouched.
	if _, err := Open(path); err == nil {
		t.Fatal("Open of a newer-schema database succeeded; want a clear refusal")
	}
	raw, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", path))
	if err != nil {
		t.Fatalf("inspect refused database: %v", err)
	}
	defer raw.Close()
	var version int
	if err := raw.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != len(migrations)+3 {
		t.Fatalf("user_version = %d, want %d (must not be rewritten)", version, len(migrations)+3)
	}
	var got string
	if err := raw.QueryRowContext(ctx, "SELECT col FROM future").Scan(&got); err != nil {
		t.Fatalf("read seeded row: %v", err)
	}
	if got != "keep" {
		t.Fatalf("seeded row = %q, want %q", got, "keep")
	}
}
