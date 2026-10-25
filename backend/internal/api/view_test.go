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
	v := BuildView(activePeering(), "pa", "team-a")
	if v.Role != "requester" || v.PeerProjectID != "pb" || v.PeerProjectName != "" || v.RouterID != routerA || v.NextHop != "100.64.0.10" {
		t.Fatalf("view %+v", v)
	}
	want := []string{"openstack router set --route destination=<peer subnet>,gateway=100.64.0.10 " + routerA}
	if !reflect.DeepEqual(v.RouteHints, want) || v.PeerRouterID != routerB {
		t.Fatalf("hints %v peer router %s", v.RouteHints, v.PeerRouterID)
	}
}

func TestBuildViewAccepter(t *testing.T) {
	v := BuildView(activePeering(), "pb", "team-a")
	if v.Role != "accepter" || v.PeerProjectID != "pa" || v.PeerProjectName != "team-a" || v.RouterID != routerB || v.NextHop != "100.64.0.9" {
		t.Fatalf("view %+v", v)
	}
	if v.RouteHints[0] != "openstack router set --route destination=<peer subnet>,gateway=100.64.0.9 "+routerB || v.PeerRouterID != routerA {
		t.Fatalf("hint %q", v.RouteHints[0])
	}
}

func TestBuildViewPendingHasNoHop(t *testing.T) {
	p := model.Peering{ID: "rpx-1", Status: model.StatusPendingAcceptance, RequesterProjectID: "pa", RequesterRouterID: routerA,
		AccepterProjectID: "pb", AccepterRouterID: routerB}
	v := BuildView(p, "pb", "team-a")
	if v.NextHop != "" || len(v.RouteHints) != 0 || v.RouterID != routerB || v.Warnings == nil {
		t.Fatalf("view %+v", v)
	}
}
