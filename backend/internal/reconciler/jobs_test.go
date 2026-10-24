package reconciler

import (
	"context"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/keystone"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/store"
)

func (e *env) pending(t *testing.T) model.Peering {
	t.Helper()
	p := &model.Peering{Name: "p", RequesterProjectID: "pa", RequesterRouterID: routerA, AccepterProjectID: "pb", CreatedBy: "ua",
		ExpiresAt: time.Now().Add(168 * time.Hour)}
	if err := e.st.CreatePeering(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return *p
}

func TestExpire(t *testing.T) {
	e := newEnv(t)
	p := e.pending(t)
	ctx := context.Background()
	e.r.Now = time.Now
	if err := e.r.ExpireOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if e.get(t, p.ID).Status != model.StatusPendingAcceptance {
		t.Fatal("expired too early")
	}
	e.r.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	if err := e.r.ExpireOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.get(t, p.ID); got.Status != model.StatusExpired {
		t.Fatalf("status %s", got.Status)
	}
	if last, _ := e.st.LastAction(ctx, p.ID); last != model.ActionExpired {
		t.Fatalf("last %q", last)
	}
}

func TestDriftDegraded(t *testing.T) {
	e := newEnv(t)
	p := e.active(t)
	e.nf.RemovePortDirect(p.AccepterPortID)
	if err := e.r.DriftOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := e.get(t, p.ID)
	if got.Status != model.StatusDegraded || !strings.Contains(got.StatusReason, routerB) {
		t.Fatalf("status %s reason %q", got.Status, got.StatusReason)
	}
}

func TestDriftProjectGoneActive(t *testing.T) {
	e := newEnv(t)
	p := e.active(t)
	e.kf.DeleteProject("pb")
	_ = e.r.DriftOnce(context.Background())
	if got := e.get(t, p.ID); got.Status != model.StatusDeleting {
		t.Fatalf("status %s", got.Status)
	}
	if last, _ := e.st.LastAction(context.Background(), p.ID); last != model.ActionProjectGone {
		t.Fatalf("last %q", last)
	}
	if got := e.drain(t, p.ID, 3); got.Status != model.StatusDeleted {
		t.Fatalf("cleanup status %s %q", got.Status, got.StatusReason)
	}
}

func TestDriftProjectGonePending(t *testing.T) {
	e := newEnv(t)
	p := e.pending(t)
	e.kf.DeleteProject("pa")
	_ = e.r.DriftOnce(context.Background())
	if got := e.get(t, p.ID); got.Status != model.StatusCancelled {
		t.Fatalf("status %s", got.Status)
	}
}

func TestDriftProjectDisabledNoChange(t *testing.T) {
	e := newEnv(t)
	p := e.active(t)
	e.kf.AddProject(keystone.Project{ID: "pb", Name: "team-b", Enabled: false})
	_ = e.r.DriftOnce(context.Background())
	if got := e.get(t, p.ID); got.Status != model.StatusActive {
		t.Fatalf("status %s", got.Status)
	}
}

func TestDriftKeystoneErrorNoChange(t *testing.T) {
	e := newEnv(t)
	p := e.active(t)
	e.kf.FailGetProject = context.DeadlineExceeded
	_ = e.r.DriftOnce(context.Background())
	if got := e.get(t, p.ID); got.Status != model.StatusActive {
		t.Fatalf("a Keystone outage must not delete peerings, status %s", got.Status)
	}
}

func TestDriftWarnings(t *testing.T) {
	e := newEnv(t)
	p := e.active(t)
	ctx := context.Background()
	e.nf.AddRouterSubnet(routerB, netip.MustParsePrefix("10.1.0.0/25"))
	_ = e.r.DriftOnce(ctx)
	got := e.get(t, p.ID)
	if got.Status != model.StatusActive || !reflect.DeepEqual(got.Warnings, []string{"10.1.0.0/24 overlaps with the peer"}) {
		t.Fatalf("status %s warnings %v", got.Status, got.Warnings)
	}
	e.nf.DeleteRouter(routerB)
	e.nf.AddRouter(routerB, "pb", netip.MustParsePrefix("10.2.0.0/24"))
	// re-attach B's port so the peering is still healthy
	_ = e.nf.AddRouterInterface(ctx, routerB, p.AccepterPortID)
	_ = e.r.DriftOnce(ctx)
	if got := e.get(t, p.ID); len(got.Warnings) != 0 {
		t.Fatalf("warnings not cleared: %v", got.Warnings)
	}
}

func TestSweepOrphan(t *testing.T) {
	e := newEnv(t)
	live := e.active(t)
	ctx := context.Background()
	e.r.Now = time.Now
	gone := e.pending(t) // a finished peering whose network was somehow left behind
	if _, err := e.st.Transition(ctx, gone.ID, []model.Status{model.StatusPendingAcceptance}, model.StatusCancelled, store.Patch{}, model.Event{Action: model.ActionCancelled}); err != nil {
		t.Fatal(err)
	}
	old := e.nf.AddNetwork(gone.ID, time.Now().Add(-2*time.Hour))
	young := e.nf.AddNetwork("rpx-000000000002", time.Now())
	foreign := e.nf.AddNetwork("not-ours", time.Now().Add(-48*time.Hour))
	if err := e.r.SweepOnce(ctx); err != nil {
		t.Fatal(err)
	}
	nets, _ := e.nf.ListOwnNetworks(ctx)
	have := map[string]bool{}
	for _, n := range nets {
		have[n.ID] = true
	}
	if have[old] || !have[young] || !have[foreign] || !have[live.TransitNetworkID] {
		t.Fatalf("after sweep: old %v young %v foreign %v live %v", have[old], have[young], have[foreign], have[live.TransitNetworkID])
	}
}

func TestPoolScanReserves(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.nf.AddRouterSubnet(routerA, netip.MustParsePrefix("100.64.0.0/28"))
	if err := e.r.PoolScanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	p := e.drain(t, e.accepted(t).ID, 3)
	if p.Status != model.StatusActive || p.TransitCIDR.Overlaps(netip.MustParsePrefix("100.64.0.0/28")) {
		t.Fatalf("status %s block %v", p.Status, p.TransitCIDR)
	}
}

func TestPoolScanFreesLeakedBlock(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	b := e.holdBlock(t)
	ps, _ := e.st.ListByStatus(ctx, model.StatusProvisioning)
	// The holder ends up deleted while still holding its block (the stale-worker race).
	if _, err := e.st.Transition(ctx, ps[0].ID, []model.Status{model.StatusProvisioning}, model.StatusDeleted, store.Patch{}, model.Event{Action: model.ActionDeleted}); err != nil {
		t.Fatal(err)
	}
	if err := e.r.PoolScanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if again := e.holdBlock(t); again != b {
		t.Fatalf("leaked block %v not freed (got %v)", b, again)
	}
}

func TestSweepSkipsNetworksWithNoRow(t *testing.T) {
	e := newEnv(t)
	e.r.Now = time.Now
	// A network with no DB row at all: a restored or wrong database, not an orphan. Leave it alone.
	unknown := e.nf.AddNetwork("rpx-0000000000aa", time.Now().Add(-48*time.Hour))
	if err := e.r.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.nf.HasNetwork(unknown) {
		t.Fatal("sweep deleted a network it has no record of")
	}
}
