// Package dbtest opens a fresh database per test on every backend available: SQLite always, Postgres when
// PHOTOSORT_TEST_PG names a server (postgres://user:pass@host:port/db), each test in its own schema.
package dbtest

import (
	"database/sql"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/mononendev/photosort/internal/db"
)

// Backends runs fn once per available backend as a subtest, with a fresh, migrated database.
func Backends(t *testing.T, fn func(t *testing.T, d *db.DB)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, SQLite(t)) })
	if os.Getenv("PHOTOSORT_TEST_PG") != "" {
		t.Run("postgres", func(t *testing.T) { fn(t, Postgres(t)) })
	}
}

// SQLite opens a new database file in a temp dir.
func SQLite(t testing.TB) *db.DB {
	t.Helper()
	d, err := db.Open("sqlite://" + filepath.Join(t.TempDir(), "photosort.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// Postgres opens a new schema on PHOTOSORT_TEST_PG, dropped when the test ends; skips when unset.
func Postgres(t testing.TB) *db.DB {
	t.Helper()
	base := os.Getenv("PHOTOSORT_TEST_PG")
	if base == "" {
		t.Skip("PHOTOSORT_TEST_PG not set")
	}
	schema := fmt.Sprintf("t_%d", rand.Uint64()%1e12)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	d, err := db.Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Close()
		if a, err := sql.Open("pgx", base); err == nil {
			a.Exec("DROP SCHEMA " + schema + " CASCADE")
			a.Close()
		}
	})
	return d
}
