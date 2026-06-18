package db

import (
	"context"
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
