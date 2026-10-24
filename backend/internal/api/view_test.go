package api

import (
	"net/netip"
	"reflect"
	"testing"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/model"
)

func activePeering() model.Peering {
	return model.Peering{ID: "rpx-1", Name: "n", Status: model.StatusActive,
		RequesterProjectID: "pa", RequesterRouterID: routerA, AccepterProjectID: "pb", AccepterRouterID: routerB,
		RequesterIP: netip.MustParseAddr("100.64.0.9"), AccepterIP: netip.MustParseAddr("100.64.0.10")}
}

func TestBuildViewRequester(t *testing.T) {
	v := BuildView(activePeering(), "pa", "team-a", []netip.Prefix{netip.MustParsePrefix("10.2.0.0/24")})
	if v.Role != "requester" || v.PeerProjectID != "pb" || v.PeerProjectName != "" || v.RouterID != routerA || v.NextHop != "100.64.0.10" {
		t.Fatalf("view %+v", v)
	}
	want := []string{"openstack router set --route destination=10.2.0.0/24,gateway=100.64.0.10 " + routerA}
	if !reflect.DeepEqual(v.RouteHints, want) || !reflect.DeepEqual(v.PeerCIDRs, []string{"10.2.0.0/24"}) {
		t.Fatalf("hints %v cidrs %v", v.RouteHints, v.PeerCIDRs)
	}
}

func TestBuildViewAccepter(t *testing.T) {
	v := BuildView(activePeering(), "pb", "team-a", []netip.Prefix{netip.MustParsePrefix("10.1.0.0/24")})
	if v.Role != "accepter" || v.PeerProjectID != "pa" || v.PeerProjectName != "team-a" || v.RouterID != routerB || v.NextHop != "100.64.0.9" {
		t.Fatalf("view %+v", v)
	}
	if v.RouteHints[0] != "openstack router set --route destination=10.1.0.0/24,gateway=100.64.0.9 "+routerB {
		t.Fatalf("hint %q", v.RouteHints[0])
	}
}

func TestBuildViewPendingHasNoHop(t *testing.T) {
	p := model.Peering{ID: "rpx-1", Status: model.StatusPendingAcceptance, RequesterProjectID: "pa", RequesterRouterID: routerA, AccepterProjectID: "pb"}
	v := BuildView(p, "pb", "team-a", nil)
	if v.NextHop != "" || len(v.RouteHints) != 0 || v.RouterID != "" || v.PeerCIDRs == nil || v.Warnings == nil {
		t.Fatalf("view %+v", v)
	}
}
