package store

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// newTestStore returns a migrated Store in a fresh schema, dropped after the test.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	base := os.Getenv("RP_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("RP_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("t_%d", rand.Uint64())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	s, err := Open(ctx, base+sep+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})
	return s
}
