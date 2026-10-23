package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
)

const (
	routerA = "aaaaaaaa-0000-0000-0000-000000000001"
	routerB = "bbbbbbbb-0000-0000-0000-000000000001"
)

func newPending(reqProject, accProject string) *model.Peering {
	return &model.Peering{
		Name: "p", RequesterProjectID: reqProject, RequesterRouterID: routerA,
		AccepterProjectID: accProject, CreatedBy: "user-a", ExpiresAt: time.Now().Add(168 * time.Hour),
	}
}

func mustCreate(t *testing.T, s *Store, p *model.Peering) model.Peering {
	t.Helper()
	if err := s.CreatePeering(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return *p
}

func TestCreateGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := mustCreate(t, s, newPending("pa", "pb"))
	if p.ID == "" {
		t.Fatal("ID not set")
	}
	got, err := s.GetPeering(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusPendingAcceptance || got.RequesterRouterID != routerA || got.AccepterRouterID != "" ||
		got.TransitCIDR.IsValid() || got.RequesterIP.IsValid() || got.CreatedBy != "user-a" || got.CreatedAt.IsZero() ||
		got.DeletedAt.IsZero() == false || len(got.Warnings) != 0 {
		t.Fatalf("unexpected row %+v", got)
	}
	var isNull bool
	if err := s.pool.QueryRow(ctx, "SELECT accepter_router_id IS NULL AND status_reason IS NULL FROM peerings WHERE id=$1", p.ID).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("empty fields must be NULL (err %v)", err)
	}
	if _, err := s.GetPeering(ctx, "rpx-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	evs, err := s.ListEvents(ctx, p.ID)
	if err != nil || len(evs) != 1 || evs[0].Action != model.ActionRequested || evs[0].ActorUserID != "user-a" {
		t.Fatalf("events %+v err %v", evs, err)
	}
}

func TestListDirection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := mustCreate(t, s, newPending("pa", "pb"))
	check := func(project, dir string, want int) {
		t.Helper()
		got, err := s.ListPeerings(ctx, ListFilter{ProjectID: project, Direction: dir})
		if err != nil || len(got) != want {
			t.Fatalf("%s/%s: got %d err %v", project, dir, len(got), err)
		}
	}
	check("pa", "outgoing", 1)
	check("pa", "incoming", 0)
	check("pb", "incoming", 1)
	check("pb", "", 1)
	check("pc", "", 0)
	all, _ := s.ListPeerings(ctx, ListFilter{All: true})
	if len(all) != 1 || all[0].ID != p.ID {
		t.Fatalf("all = %+v", all)
	}
	got, _ := s.ListPeerings(ctx, ListFilter{ProjectID: "pa", Statuses: []model.Status{model.StatusActive}})
	if len(got) != 0 {
		t.Fatal("status filter ignored")
	}
}

func TestListPagination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		mustCreate(t, s, newPending("pa", "pb"))
	}
	first, _ := s.ListPeerings(ctx, ListFilter{ProjectID: "pa", Limit: 2})
	rest, _ := s.ListPeerings(ctx, ListFilter{ProjectID: "pa", Limit: 2, Marker: first[1].ID})
	if len(first) != 2 || len(rest) != 1 || rest[0].ID == first[0].ID || rest[0].ID == first[1].ID {
		t.Fatalf("pagination broken: %d %d", len(first), len(rest))
	}
}

func TestTransitionConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := mustCreate(t, s, newPending("pa", "pb"))
	rb := routerB
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = s.Transition(ctx, p.ID, []model.Status{model.StatusPendingAcceptance}, model.StatusProvisioning,
			Patch{AccepterRouterID: &rb}, model.Event{Action: model.ActionAccepted})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = s.Transition(ctx, p.ID, []model.Status{model.StatusPendingAcceptance}, model.StatusCancelled,
			Patch{}, model.Event{Action: model.ActionCancelled})
	}()
	wg.Wait()
	ok := 0
	for _, e := range errs {
		if e == nil {
			ok++
		} else if !errors.Is(e, ErrConflict) {
			t.Fatalf("unexpected error %v", e)
		}
	}
	if ok != 1 {
		t.Fatalf("exactly one transition must win, got %d (%v)", ok, errs)
	}
	if _, err := s.Transition(ctx, "rpx-missing", []model.Status{model.StatusActive}, model.StatusDeleting, Patch{}, model.Event{Action: model.ActionDeleteRequested}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestTransitionDuplicatePairConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rb := routerB
	for i, proj := range []string{"pb", "pb"} {
		p := mustCreate(t, s, newPending("pa", proj))
		_, err := s.Transition(ctx, p.ID, []model.Status{model.StatusPendingAcceptance}, model.StatusProvisioning, Patch{AccepterRouterID: &rb}, model.Event{Action: model.ActionAccepted})
		if i == 1 && !errors.Is(err, ErrConflict) {
			t.Fatalf("second accept on same pair: want ErrConflict, got %v", err)
		}
	}
}

func TestTransitionWritesEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := mustCreate(t, s, newPending("pa", "pb"))
	reason := "bye"
	by := "user-a"
	got, err := s.Transition(ctx, p.ID, []model.Status{model.StatusPendingAcceptance}, model.StatusCancelled,
		Patch{StatusReason: &reason, DeletedBy: &by}, model.Event{Action: model.ActionCancelled, ActorUserID: by, ActorProjectID: "pa"})
	if err != nil || got.Status != model.StatusCancelled || got.StatusReason != "bye" || got.DeletedBy != by {
		t.Fatalf("got %+v err %v", got, err)
	}
	last, err := s.LastAction(ctx, p.ID)
	if err != nil || last != model.ActionCancelled {
		t.Fatalf("last action %q err %v", last, err)
	}
	last, _ = s.LastAction(ctx, p.ID, model.ActionCancelled)
	if last != model.ActionRequested {
		t.Fatalf("skip ignored: %q", last)
	}
}

func TestCountNonTerminal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, newPending("pa", "pb"))
	p2 := mustCreate(t, s, newPending("pc", "pa"))
	p3 := mustCreate(t, s, newPending("pa", "pd"))
	if _, err := s.Transition(ctx, p3.ID, []model.Status{model.StatusPendingAcceptance}, model.StatusRejected, Patch{}, model.Event{Action: model.ActionRejected}); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountNonTerminal(ctx, "pa")
	if err != nil || n != 2 {
		t.Fatalf("count = %d err %v (p2 %s)", n, err, p2.ID)
	}
	byStatus, _ := s.ListByStatus(ctx, model.StatusRejected)
	if len(byStatus) != 1 || byStatus[0].ID != p3.ID {
		t.Fatalf("ListByStatus = %+v", byStatus)
	}
}
