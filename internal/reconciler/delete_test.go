package reconciler

import (
	"context"
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/neutron"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/store"
)

func (e *env) active(t *testing.T) model.Peering {
	t.Helper()
	p := e.drain(t, e.accepted(t).ID, 3)
	if p.Status != model.StatusActive {
		t.Fatalf("setup: status %s", p.Status)
	}
	return p
}

func (e *env) requestDelete(t *testing.T, p model.Peering, from model.Status) {
	t.Helper()
	by := "ua"
	if _, err := e.st.Transition(context.Background(), p.ID, []model.Status{from}, model.StatusDeleting,
		store.Patch{DeletedBy: &by}, model.Event{Action: model.ActionDeleteRequested, ActorUserID: by}); err != nil {
		t.Fatal(err)
	}
}

func countEvents(t *testing.T, e *env, id, action string) int {
	t.Helper()
	evs, err := e.st.ListEvents(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Action == action {
			n++
		}
	}
	return n
}

func TestDeleteCleansEverything(t *testing.T) {
	e := newEnv(t)
	p := e.active(t)
	e.requestDelete(t, p, model.StatusActive)
	got := e.drain(t, p.ID, 3)
	if got.Status != model.StatusDeleted || got.DeletedAt.IsZero() || got.DeletedBy != "ua" {
		t.Fatalf("got %+v", got)
	}
	if nets, subs, ports := e.nf.Counts(); nets+subs+ports != 0 {
		t.Fatalf("leftovers %d %d %d", nets, subs, ports)
	}
	ctx := context.Background()
	q := &model.Peering{Name: "q", RequesterProjectID: "pa", RequesterRouterID: routerA, AccepterProjectID: "pb", CreatedBy: "ua"}
	_ = e.st.CreatePeering(ctx, q)
	if b, _ := e.st.AllocateBlock(ctx, q.ID); b != p.TransitCIDR {
		t.Fatalf("block not freed, next got %v", b)
	}
	if countEvents(t, e, p.ID, model.ActionDeleted) != 1 {
		t.Fatal("deleted event missing")
	}
}

func TestDeleteBlockedByRoute(t *testing.T) {
	e := newEnv(t)
	p := e.active(t)
	e.nf.SetRoute(routerA, p.AccepterIP) // A routes to B through B's transit IP
	e.requestDelete(t, p, model.StatusActive)
	for i := 0; i < 3; i++ {
		_ = e.r.WorkOnce(context.Background())
	}
	got := e.get(t, p.ID)
	if got.Status != model.StatusDeleting || !strings.Contains(got.StatusReason, p.AccepterIP.String()) || !strings.Contains(got.StatusReason, routerA) {
		t.Fatalf("got status %s reason %q", got.Status, got.StatusReason)
	}
	if n := countEvents(t, e, p.ID, model.ActionDeleteBlocked); n != 1 {
		t.Fatalf("delete_blocked events = %d, want 1", n)
	}
	e.nf.ClearRoutes(routerA)
	if got := e.drain(t, p.ID, 3); got.Status != model.StatusDeleted {
		t.Fatalf("after route removed: %s %q", got.Status, got.StatusReason)
	}
}

func TestDeleteToleratesMissing(t *testing.T) {
	e := newEnv(t)
	p := e.active(t)
	e.nf.RemovePortDirect(p.RequesterPortID)
	e.nf.RemovePortDirect(p.AccepterPortID)
	e.requestDelete(t, p, model.StatusActive)
	if got := e.drain(t, p.ID, 3); got.Status != model.StatusDeleted {
		t.Fatalf("got %s %q", got.Status, got.StatusReason)
	}
	if nets, subs, ports := e.nf.Counts(); nets+subs+ports != 0 {
		t.Fatalf("leftovers %d %d %d", nets, subs, ports)
	}
}

func TestDeleteDuringProvisioning(t *testing.T) {
	e := newEnv(t)
	e.nf.FailNext("AddRouterInterface", &neutron.APIError{Code: 503, Message: "busy"})
	p := e.accepted(t)
	if err := e.r.WorkOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mid := e.get(t, p.ID); mid.Status != model.StatusProvisioning || mid.RequesterPortID == "" {
		t.Fatalf("setup: expected a half-built peering, got %+v", mid)
	}
	e.requestDelete(t, p, model.StatusProvisioning)
	if got := e.drain(t, p.ID, 3); got.Status != model.StatusDeleted {
		t.Fatalf("got %s %q", got.Status, got.StatusReason)
	}
	if nets, subs, ports := e.nf.Counts(); nets+subs+ports != 0 {
		t.Fatalf("leftovers %d %d %d", nets, subs, ports)
	}
}
