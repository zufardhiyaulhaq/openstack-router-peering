package reconciler

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/neutron"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/store"
)

func TestProvisionHappyPath(t *testing.T) {
	e := newEnv(t)
	p := e.drain(t, e.accepted(t).ID, 3)
	if p.Status != model.StatusActive {
		t.Fatalf("status %s reason %q", p.Status, p.StatusReason)
	}
	if p.TransitCIDR != netip.MustParsePrefix("100.64.0.0/29") || p.RequesterIP != netip.MustParseAddr("100.64.0.1") || p.AccepterIP != netip.MustParseAddr("100.64.0.2") {
		t.Fatalf("addressing %v %v %v", p.TransitCIDR, p.RequesterIP, p.AccepterIP)
	}
	if nets, subs, ports := e.nf.Counts(); nets != 1 || subs != 1 || ports != 2 {
		t.Fatalf("counts %d %d %d", nets, subs, ports)
	}
	ctx := context.Background()
	pa, _ := e.nf.GetPort(ctx, p.RequesterPortID)
	pb, _ := e.nf.GetPort(ctx, p.AccepterPortID)
	if pa.ProjectID != "pa" || pa.DeviceID != routerA || pa.IP != p.RequesterIP || pb.ProjectID != "pb" || pb.DeviceID != routerB {
		t.Fatalf("ports %+v %+v", pa, pb)
	}
	if n, err := e.nf.FindNetworkByName(ctx, p.ID); err != nil || n.ID != p.TransitNetworkID || n.Tags[0] != p.ID {
		t.Fatalf("network %+v err %v", n, err)
	}
	if last, _ := e.st.LastAction(ctx, p.ID); last != model.ActionProvisioned {
		t.Fatalf("last event %q", last)
	}
	if p.ClaimedBy != "" || p.Attempts != 0 || p.StatusReason != "" {
		t.Fatalf("claim not cleared %+v", p)
	}
}

func TestProvisionResumesAfterCrash(t *testing.T) {
	for _, method := range []string{"CreateNetwork", "CreateSubnet", "CreatePort", "AddRouterInterface"} {
		t.Run(method, func(t *testing.T) {
			e := newEnv(t)
			e.nf.FailNext(method, &neutron.APIError{Code: 503, Message: "busy"})
			p := e.accepted(t)
			got := e.drain(t, p.ID, 5)
			if got.Status != model.StatusActive {
				t.Fatalf("status %s reason %q", got.Status, got.StatusReason)
			}
			if nets, subs, ports := e.nf.Counts(); nets != 1 || subs != 1 || ports != 2 {
				t.Fatalf("duplicates after resume: %d %d %d", nets, subs, ports)
			}
		})
	}
}

func TestProvisionTransientRecordsRetry(t *testing.T) {
	e := newEnv(t)
	e.nf.FailNext("CreateNetwork", &neutron.APIError{Code: 503, Message: "busy"})
	p := e.accepted(t)
	if err := e.r.WorkOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := e.get(t, p.ID)
	if got.Status != model.StatusProvisioning || got.Attempts != 1 || !strings.Contains(got.StatusReason, "busy") || got.ClaimedBy != "" {
		t.Fatalf("after transient %+v", got)
	}
}

func TestProvisionCrashBeforeIDSaved(t *testing.T) {
	e := newEnv(t)
	p := e.accepted(t)
	ctx := context.Background()
	// Simulate a crash after Neutron created the network and port but before the IDs reached the DB.
	net, _ := e.nf.CreateNetwork(ctx, p.ID, p.ID)
	sub, _ := e.nf.CreateSubnet(ctx, net, p.ID, p.ID, netip.MustParsePrefix("100.64.0.0/29"))
	if _, err := e.nf.CreatePort(ctx, net, sub, "pa", p.ID+"-requester", p.ID, netip.MustParseAddr("100.64.0.1")); err != nil {
		t.Fatal(err)
	}
	got := e.drain(t, p.ID, 3)
	if got.Status != model.StatusActive || got.TransitNetworkID != net {
		t.Fatalf("status %s net %s", got.Status, got.TransitNetworkID)
	}
	if nets, subs, ports := e.nf.Counts(); nets != 1 || subs != 1 || ports != 2 {
		t.Fatalf("counts %d %d %d", nets, subs, ports)
	}
}

func TestProvisionHardErrorRollsBack(t *testing.T) {
	e := newEnv(t)
	e.nf.AddRouterSubnet(routerB, netip.MustParsePrefix("100.64.0.0/28")) // overlaps the first transit block
	p := e.drain(t, e.accepted(t).ID, 3)
	if p.Status != model.StatusFailed || !strings.Contains(p.StatusReason, "overlaps") {
		t.Fatalf("status %s reason %q", p.Status, p.StatusReason)
	}
	if nets, subs, ports := e.nf.Counts(); nets+subs+ports != 0 {
		t.Fatalf("leftovers %d %d %d", nets, subs, ports)
	}
	if last, _ := e.st.LastAction(context.Background(), p.ID); last != model.ActionError {
		t.Fatalf("last event %q", last)
	}
	// The block is free again: a new peering gets the same /29.
	ctx := context.Background()
	q := &model.Peering{Name: "q", RequesterProjectID: "pa", RequesterRouterID: routerA, AccepterProjectID: "pb", CreatedBy: "ua"}
	_ = e.st.CreatePeering(ctx, q)
	if b, err := e.st.AllocateBlock(ctx, q.ID); err != nil || b != netip.MustParsePrefix("100.64.0.0/29") {
		t.Fatalf("block not freed: %v %v", b, err)
	}
}

func TestProvisionRouterGone(t *testing.T) {
	e := newEnv(t)
	e.nf.DeleteRouter(routerB)
	p := e.drain(t, e.accepted(t).ID, 3)
	if p.Status != model.StatusFailed || !strings.Contains(p.StatusReason, routerB) {
		t.Fatalf("status %s reason %q", p.Status, p.StatusReason)
	}
}

func TestProvisionPoolExhausted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := 0; i < 32; i++ { // hold every block of the /24
		q := &model.Peering{Name: "q", RequesterProjectID: "pa", RequesterRouterID: routerA, AccepterProjectID: "pb", CreatedBy: "ua"}
		_ = e.st.CreatePeering(ctx, q)
		if _, err := e.st.AllocateBlock(ctx, q.ID); err != nil {
			t.Fatal(err)
		}
	}
	p := e.accepted(t)
	_ = e.r.WorkOnce(ctx)
	got := e.get(t, p.ID)
	if got.Status != model.StatusProvisioning || got.StatusReason != "transit pool exhausted" || got.Attempts != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestRepairRecreatesMissingSide(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.drain(t, e.accepted(t).ID, 3)
	e.nf.RemovePortDirect(p.RequesterPortID)
	degraded, err := e.st.Transition(ctx, p.ID, []model.Status{model.StatusActive}, model.StatusDegraded, store.Patch{}, model.Event{Action: model.ActionDegraded})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Transition(ctx, degraded.ID, []model.Status{model.StatusDegraded}, model.StatusProvisioning, store.Patch{ResetAttempts: true}, model.Event{Action: model.ActionRepairRequested}); err != nil {
		t.Fatal(err)
	}
	got := e.drain(t, p.ID, 3)
	if got.Status != model.StatusActive || got.AccepterPortID != p.AccepterPortID || got.RequesterPortID == p.RequesterPortID {
		t.Fatalf("repair result %+v", got)
	}
	port, _ := e.nf.GetPort(ctx, got.RequesterPortID)
	if port.IP != p.RequesterIP || port.DeviceID != routerA {
		t.Fatalf("new port %+v", port)
	}
	if last, _ := e.st.LastAction(ctx, p.ID); last != model.ActionRepaired {
		t.Fatalf("last event %q", last)
	}
}

func TestBackoff(t *testing.T) {
	for attempts, want := range map[int]time.Duration{0: 5 * time.Second, 1: 10 * time.Second, 10: 5 * time.Minute, 100: 5 * time.Minute} {
		if got := Backoff(attempts, 5*time.Second, 5*time.Minute); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", attempts, got, want)
		}
	}
}

func TestProvisionIgnoresTenantLookalikes(t *testing.T) {
	e := newEnv(t)
	p := e.accepted(t)
	ctx := context.Background()
	// Project A knows the peering ID before B accepts and builds its own network and subnet with that name.
	tenantNet := e.nf.AddTenantNetwork(p.ID, "pa")
	e.nf.AddTenantSubnet(tenantNet, p.ID, "pa", netip.MustParsePrefix("100.64.0.0/29"))
	got := e.drain(t, p.ID, 3)
	if got.Status != model.StatusActive || got.TransitNetworkID == tenantNet {
		t.Fatalf("status %s transit network %s (tenant's is %s)", got.Status, got.TransitNetworkID, tenantNet)
	}
	if port, _ := e.nf.GetPort(ctx, got.AccepterPortID); port.NetworkID != got.TransitNetworkID {
		t.Fatalf("B's port landed on %s", port.NetworkID)
	}
	if _, err := e.st.Transition(ctx, p.ID, []model.Status{model.StatusActive}, model.StatusDeleting, store.Patch{}, model.Event{Action: model.ActionDeleteRequested}); err != nil {
		t.Fatal(err)
	}
	if got := e.drain(t, p.ID, 3); got.Status != model.StatusDeleted {
		t.Fatalf("delete: %s", got.Status)
	}
	if !e.nf.HasNetwork(tenantNet) {
		t.Fatal("teardown deleted the tenant's own network")
	}
}
