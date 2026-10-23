package store

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
)

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
		ids[i] = mustCreate(t, s, newPending("pa", "pb")).ID
	}
	var wg sync.WaitGroup
	got := make([]netip.Prefix, 32)
	errs := make([]error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = s.AllocateBlock(ctx, ids[i])
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
	if _, err := s.AllocateBlock(ctx, ids[32]); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("want ErrPoolExhausted, got %v", err)
	}
}

func TestAllocateIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _ = s.SeedBlocks(ctx, netip.MustParsePrefix("100.64.0.0/28"))
	id := mustCreate(t, s, newPending("pa", "pb")).ID
	a, err1 := s.AllocateBlock(ctx, id)
	b, err2 := s.AllocateBlock(ctx, id)
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
		p, err := s.AllocateBlock(ctx, mustCreate(t, s, newPending("pa", "pb")).ID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Overlaps(netip.MustParsePrefix("100.64.0.0/28")) {
			t.Fatalf("reserved block %v handed out", p)
		}
	}
	if _, err := s.AllocateBlock(ctx, mustCreate(t, s, newPending("pa", "pb")).ID); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("want exhausted while reserved, got %v", err)
	}
	if n, _ := s.SetReserved(ctx, nil); n != 0 {
		t.Fatalf("reserved after clear = %d", n)
	}
	if _, err := s.AllocateBlock(ctx, mustCreate(t, s, newPending("pa", "pb")).ID); err != nil {
		t.Fatalf("unreserved block must be allocatable: %v", err)
	}
}

func TestFreeBlock(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _ = s.SeedBlocks(ctx, netip.MustParsePrefix("100.64.0.0/29"))
	a := mustCreate(t, s, newPending("pa", "pb")).ID
	b := mustCreate(t, s, newPending("pa", "pb")).ID
	if _, err := s.AllocateBlock(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.FreeBlock(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllocateBlock(ctx, b); err != nil {
		t.Fatalf("freed block not reusable: %v", err)
	}
}
