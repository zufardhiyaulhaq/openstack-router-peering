package store

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/model"
)

// claimed creates a provisioning peering leased to worker "w", as the reconciler holds it while allocating.
func claimed(t *testing.T, s *Store) string {
	t.Helper()
	id := provisioningPeering(t, s, routerN(int(nextRouter.Add(1)))).ID
	if _, err := s.pool.Exec(context.Background(), `UPDATE peerings SET claimed_by = 'w', claimed_until = now() + interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

var nextRouter atomic.Int64

func TestAllocateRequiresLease(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _ = s.SeedBlocks(ctx, netip.MustParsePrefix("100.64.0.0/29"))
	pending := mustCreate(t, s, newPending("pa", "pb")).ID
	if _, err := s.AllocateBlock(ctx, pending, "w"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("pending row: want ErrLeaseLost, got %v", err)
	}
	id := claimed(t, s)
	if _, err := s.AllocateBlock(ctx, id, "other"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("someone else's lease: want ErrLeaseLost, got %v", err)
	}
	if _, err := s.Transition(ctx, id, []model.Status{model.StatusProvisioning}, model.StatusDeleting, Patch{}, model.Event{Action: model.ActionDeleteRequested}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllocateBlock(ctx, id, "w"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("row moved to deleting: want ErrLeaseLost, got %v", err)
	}
	var held int
	_ = s.pool.QueryRow(ctx, "SELECT count(*) FROM transit_blocks WHERE peering_id IS NOT NULL").Scan(&held)
	if held != 0 {
		t.Fatalf("%d blocks held after refused allocations", held)
	}
}

func TestFreeTerminalBlocks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _ = s.SeedBlocks(ctx, netip.MustParsePrefix("100.64.0.0/28"))
	leaked, live := claimed(t, s), claimed(t, s)
	_, _ = s.AllocateBlock(ctx, leaked, "w")
	_, _ = s.AllocateBlock(ctx, live, "w")
	if _, err := s.pool.Exec(ctx, `UPDATE peerings SET status = 'deleted' WHERE id = $1`, leaked); err != nil {
		t.Fatal(err)
	}
	n, err := s.FreeTerminalBlocks(ctx)
	if err != nil || n != 1 {
		t.Fatalf("freed %d err %v", n, err)
	}
	var holder string
	_ = s.pool.QueryRow(ctx, "SELECT coalesce(string_agg(peering_id, ','), '') FROM transit_blocks WHERE peering_id IS NOT NULL").Scan(&holder)
	if holder != live {
		t.Fatalf("held by %q, want only %q", holder, live)
	}
}

func TestSeedIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	n, err := s.SeedBlocks(ctx, netip.MustParsePrefix("100.64.0.0/24"))
	if err != nil || n != 32 {
		t.Fatalf("seed = %d err %v", n, err)
	}
	n, err = s.SeedBlocks(ctx, netip.MustParsePrefix("100.64.0.0/24"))
	if err != nil || n != 0 {
		t.Fatalf("reseed = %d err %v", n, err)
	}
}

func TestAllocateConcurrent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.SeedBlocks(ctx, netip.MustParsePrefix("100.64.0.0/24")); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 33)
	for i := range ids {
		ids[i] = claimed(t, s)
	}
	var wg sync.WaitGroup
	got := make([]netip.Prefix, 32)
	errs := make([]error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = s.AllocateBlock(ctx, ids[i], "w")
		}(i)
	}
	wg.Wait()
	seen := map[netip.Prefix]bool{}
	for i, p := range got {
		if errs[i] != nil {
			t.Fatalf("allocate %d: %v", i, errs[i])
		}
		if p.Bits() != 29 || seen[p] {
			t.Fatalf("bad or duplicate block %v", p)
		}
		seen[p] = true
	}
	if _, err := s.AllocateBlock(ctx, ids[32], "w"); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("want ErrPoolExhausted, got %v", err)
	}
}

func TestAllocateIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _ = s.SeedBlocks(ctx, netip.MustParsePrefix("100.64.0.0/28"))
	id := claimed(t, s)
	a, err1 := s.AllocateBlock(ctx, id, "w")
	b, err2 := s.AllocateBlock(ctx, id, "w")
	if err1 != nil || err2 != nil || a != b {
		t.Fatalf("%v %v %v %v", a, b, err1, err2)
	}
}

func TestReservedSkipped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _ = s.SeedBlocks(ctx, netip.MustParsePrefix("100.64.0.0/27"))
	n, err := s.SetReserved(ctx, []netip.Prefix{netip.MustParsePrefix("100.64.0.0/28")})
	if err != nil || n != 2 {
		t.Fatalf("reserved = %d err %v", n, err)
	}
	for i := 0; i < 2; i++ {
		p, err := s.AllocateBlock(ctx, claimed(t, s), "w")
		if err != nil {
			t.Fatal(err)
		}
		if p.Overlaps(netip.MustParsePrefix("100.64.0.0/28")) {
			t.Fatalf("reserved block %v handed out", p)
		}
	}
	if _, err := s.AllocateBlock(ctx, claimed(t, s), "w"); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("want exhausted while reserved, got %v", err)
	}
	if n, _ := s.SetReserved(ctx, nil); n != 0 {
		t.Fatalf("reserved after clear = %d", n)
	}
	if _, err := s.AllocateBlock(ctx, claimed(t, s), "w"); err != nil {
		t.Fatalf("unreserved block must be allocatable: %v", err)
	}
}

func TestFreeBlock(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _ = s.SeedBlocks(ctx, netip.MustParsePrefix("100.64.0.0/29"))
	a, b := claimed(t, s), claimed(t, s)
	if _, err := s.AllocateBlock(ctx, a, "w"); err != nil {
		t.Fatal(err)
	}
	if err := s.FreeBlock(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllocateBlock(ctx, b, "w"); err != nil {
		t.Fatalf("freed block not reusable: %v", err)
	}
}
