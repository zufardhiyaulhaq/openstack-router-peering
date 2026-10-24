package fake

import (
	"context"
	"net/netip"
	"testing"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/neutron"
)

var _ neutron.Client = (*Fake)(nil)

func setup(t *testing.T) (*Fake, string, string) {
	t.Helper()
	f := New()
	f.AddRouter("ra", "pa", netip.MustParsePrefix("10.0.0.0/24"))
	ctx := context.Background()
	net, _ := f.CreateNetwork(ctx, "rpx-1", "rpx-1")
	sub, _ := f.CreateSubnet(ctx, net, "rpx-1", "rpx-1", netip.MustParsePrefix("100.64.0.8/29"))
	port, err := f.CreatePort(ctx, net, sub, "pa", "rpx-1-requester", "rpx-1", netip.MustParseAddr("100.64.0.9"))
	if err != nil {
		t.Fatal(err)
	}
	return f, net, port
}

func TestFakeAttachAndRouter(t *testing.T) {
	f, _, port := setup(t)
	ctx := context.Background()
	if err := f.AddRouterInterface(ctx, "ra", port); err != nil {
		t.Fatal(err)
	}
	p, _ := f.GetPort(ctx, port)
	if p.DeviceID != "ra" || p.DeviceOwner != "network:router_interface" {
		t.Fatalf("port %+v", p)
	}
	r, _ := f.GetRouter(ctx, "ra")
	if len(r.SubnetCIDRs) != 1 {
		t.Fatalf("transit subnet must not show in SubnetCIDRs: %v", r.SubnetCIDRs)
	}
}

func TestFakeRemoveBlockedByRoute(t *testing.T) {
	f, _, port := setup(t)
	ctx := context.Background()
	_ = f.AddRouterInterface(ctx, "ra", port)
	f.SetRoute("ra", netip.MustParseAddr("100.64.0.9"))
	if err := f.RemoveRouterInterface(ctx, "ra", port); neutron.KindOf(err) != neutron.KindInUseByRoute {
		t.Fatalf("want in-use-by-route, got %v", err)
	}
	f.ClearRoutes("ra")
	if err := f.RemoveRouterInterface(ctx, "ra", port); err != nil {
		t.Fatal(err)
	}
	if _, err := f.GetPort(ctx, port); neutron.KindOf(err) != neutron.KindNotFound {
		t.Fatal("remove_router_interface must delete the port")
	}
}

func TestFakeOverlapOnAttach(t *testing.T) {
	f := New()
	ctx := context.Background()
	f.AddRouter("rb", "pb", netip.MustParsePrefix("100.64.0.0/24"))
	net, _ := f.CreateNetwork(ctx, "rpx-1", "rpx-1")
	sub, _ := f.CreateSubnet(ctx, net, "rpx-1", "rpx-1", netip.MustParsePrefix("100.64.0.8/29"))
	port, _ := f.CreatePort(ctx, net, sub, "pb", "rpx-1-accepter", "rpx-1", netip.MustParseAddr("100.64.0.10"))
	if err := f.AddRouterInterface(ctx, "rb", port); neutron.KindOf(err) != neutron.KindHard {
		t.Fatalf("want hard overlap error, got %v", err)
	}
}

func TestFakeFailNext(t *testing.T) {
	f := New()
	f.FailNext("CreateNetwork", &neutron.APIError{Code: 503})
	if _, err := f.CreateNetwork(context.Background(), "rpx-1", "rpx-1"); neutron.KindOf(err) != neutron.KindTransient {
		t.Fatalf("got %v", err)
	}
	if _, err := f.CreateNetwork(context.Background(), "rpx-1", "rpx-1"); err != nil {
		t.Fatal("failure must fire once")
	}
}
