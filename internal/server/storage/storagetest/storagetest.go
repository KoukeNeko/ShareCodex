// Package storagetest opens a throwaway server database for tests.
package storagetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
)

// New creates a fresh database on the server named by TEST_DATABASE_URL,
// e.g. postgres://postgres:test@localhost:55432/sharecodex?sslmode=disable,
// and drops it when the test ends. Tests are skipped without it.
func New(t *testing.T) *storage.Store {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	rand.Read(b)
	name := "sharecodex_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	store, err := storage.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Close()
		admin.Exec("DROP DATABASE " + name + " WITH (FORCE)")
		admin.Close()
	})
	return store
}
