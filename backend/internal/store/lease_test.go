package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/model"
)

// provisioningPeering creates a peering and moves it to provisioning with a unique accepter router.
func provisioningPeering(t *testing.T, s *Store, accRouter string) model.Peering {
	t.Helper()
	p := mustCreate(t, s, newPending("pa", "pb"))
	got, err := s.Transition(context.Background(), p.ID, []model.Status{model.StatusPendingAcceptance}, model.StatusProvisioning,
		Patch{AccepterRouterID: &accRouter}, model.Event{Action: model.ActionAccepted})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func routerN(i int) string { return fmt.Sprintf("cccccccc-0000-0000-0000-%012d", i) }

func TestClaimNoOverlap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		provisioningPeering(t, s, routerN(i))
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for _, me := range []string{"w1", "w2"} {
		wg.Add(1)
		go func(me string) {
			defer wg.Done()
			got, err := s.ClaimWork(ctx, me, 10, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, p := range got {
				if seen[p.ID] {
					t.Errorf("%s claimed %s twice", me, p.ID)
				}
				if p.ClaimedBy != me {
					t.Errorf("ClaimedBy = %q", p.ClaimedBy)
				}
				seen[p.ID] = true
			}
		}(me)
	}
	wg.Wait()
	if len(seen) != 20 {
		t.Fatalf("claimed %d of 20", len(seen))
	}
}

func TestExpiredLeaseReclaimable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := provisioningPeering(t, s, routerB)
	if got, _ := s.ClaimWork(ctx, "w1", 10, time.Millisecond); len(got) != 1 {
		t.Fatal("w1 did not claim")
	}
	time.Sleep(20 * time.Millisecond)
	got, err := s.ClaimWork(ctx, "w2", 10, time.Minute)
	if err != nil || len(got) != 1 || got[0].ID != p.ID {
		t.Fatalf("w2 got %v err %v", got, err)
	}
	if err := s.ExtendLease(ctx, p.ID, "w1", time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("w1 extend: want ErrLeaseLost, got %v", err)
	}
}

func TestStaleWorkerCannotWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := provisioningPeering(t, s, routerB)
	if got, _ := s.ClaimWork(ctx, "w1", 10, time.Minute); len(got) != 1 {
		t.Fatal("not claimed")
	}
	if _, err := s.Transition(ctx, p.ID, []model.Status{model.StatusProvisioning}, model.StatusDeleting, Patch{}, model.Event{Action: model.ActionDeleteRequested}); err != nil {
		t.Fatal(err)
	}
	net := "11111111-2222-3333-4444-555555555555"
	if err := s.SaveStep(ctx, p.ID, "w1", model.StatusProvisioning, WorkerPatch{TransitNetworkID: &net}, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("want ErrLeaseLost, got %v", err)
	}
}

func TestNotDueNotClaimed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := provisioningPeering(t, s, routerB)
	got, _ := s.ClaimWork(ctx, "w1", 10, time.Minute)
	if len(got) != 1 {
		t.Fatal("not claimed")
	}
	if err := s.Retry(ctx, p.ID, "w1", model.StatusProvisioning, "busy", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ClaimWork(ctx, "w2", 10, time.Minute); len(got) != 0 {
		t.Fatal("future next_attempt_at must not be claimed")
	}
	row, _ := s.GetPeering(ctx, p.ID)
	if row.Attempts != 1 || row.StatusReason != "busy" || row.ClaimedBy != "" {
		t.Fatalf("retry fields %+v", row)
	}
}

func TestSaveStepAndFinish(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := provisioningPeering(t, s, routerB)
	_, _ = s.ClaimWork(ctx, "w1", 10, time.Minute)
	cidr := netip.MustParsePrefix("100.64.0.8/29")
	rip, aip := netip.MustParseAddr("100.64.0.9"), netip.MustParseAddr("100.64.0.10")
	if err := s.SaveStep(ctx, p.ID, "w1", model.StatusProvisioning, WorkerPatch{TransitCIDR: &cidr, RequesterIP: &rip, AccepterIP: &aip},
		&model.Event{Action: model.ActionProvisionStep, Detail: map[string]any{"step": "allocate"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, p.ID, "w1", model.StatusProvisioning, model.StatusActive, WorkerPatch{}, model.Event{Action: model.ActionProvisioned}); err != nil {
		t.Fatal(err)
	}
	row, _ := s.GetPeering(ctx, p.ID)
	if row.Status != model.StatusActive || row.TransitCIDR != cidr || row.AccepterIP != aip || row.ClaimedBy != "" || row.Attempts != 0 {
		t.Fatalf("row %+v", row)
	}
	evs, _ := s.ListEvents(ctx, p.ID)
	if evs[len(evs)-1].Action != model.ActionProvisioned || evs[len(evs)-2].Detail["step"] != "allocate" {
		t.Fatalf("events %+v", evs)
	}
}

func TestClaimWorkSkips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := provisioningPeering(t, s, routerB)
	if got, _ := s.ClaimWork(ctx, "w1", 10, time.Minute, p.ID); len(got) != 0 {
		t.Fatalf("skipped row was claimed: %v", got)
	}
	if got, _ := s.ClaimWork(ctx, "w1", 10, time.Minute); len(got) != 1 {
		t.Fatal("row not claimable without skip")
	}
}
