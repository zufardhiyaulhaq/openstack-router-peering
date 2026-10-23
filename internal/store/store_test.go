package store

import (
	"context"
	"testing"
)

func TestMigrateIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("schema_migrations rows = %d", n)
	}
}

const insertRaw = `INSERT INTO peerings (id, name, status, requester_project_id, requester_router_id, accepter_project_id, accepter_router_id, created_by)
VALUES ($1, 'n', $2, $3, $4, $5, $6, 'u')`

func TestSameProjectRejected(t *testing.T) {
	s := newTestStore(t)
	_, err := s.pool.Exec(context.Background(), insertRaw, "rpx-1", "pending_acceptance", "pa", "11111111-1111-1111-1111-111111111111", "pa", nil)
	if err == nil {
		t.Fatal("expected check violation")
	}
}

func TestRouterPairIndex(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := "11111111-1111-1111-1111-111111111111"
	sr := "22222222-2222-2222-2222-222222222222"
	if _, err := s.pool.Exec(ctx, insertRaw, "rpx-1", "pending_acceptance", "pa", r, "pb", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, insertRaw, "rpx-2", "pending_acceptance", "pa", r, "pc", nil); err != nil {
		t.Fatalf("2 pending requests from one router must both insert: %v", err)
	}
	set := `UPDATE peerings SET accepter_router_id = $2, status = 'provisioning' WHERE id = $1`
	if _, err := s.pool.Exec(ctx, set, "rpx-1", sr); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, set, "rpx-2", sr); err == nil {
		t.Fatal("expected unique violation on the router pair")
	}
	if _, err := s.pool.Exec(ctx, `UPDATE peerings SET status = 'deleted' WHERE id = 'rpx-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, set, "rpx-2", sr); err != nil {
		t.Fatalf("after delete the pair must be free: %v", err)
	}
}
