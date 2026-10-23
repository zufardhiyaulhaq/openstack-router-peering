package api

import (
	"context"
	"net/netip"
	"sync"
	"testing"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/keystone"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/store"
)

func (e *tenv) accept(t *testing.T, id, token, router string, out any) int {
	t.Helper()
	return e.do(t, "POST", "/v1/peerings/"+id+"/accept", token, map[string]string{"router_id": router}, out)
}

func (e *tenv) status(t *testing.T, id string) model.Status {
	t.Helper()
	p, err := e.st.GetPeering(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Status
}

// setStatus forces a status for tests that need active/degraded rows without running the reconciler.
func (e *tenv) setStatus(t *testing.T, id string, from, to model.Status) {
	t.Helper()
	if _, err := e.st.Transition(context.Background(), id, []model.Status{from}, to, store.Patch{}, model.Event{Action: model.ActionProvisionStep}); err != nil {
		t.Fatal(err)
	}
}

func TestAccept202(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	var out View
	if code := e.accept(t, v.ID, "tok-b", routerB, &out); code != 202 || out.Status != "provisioning" || out.Role != "accepter" || out.RouterID != routerB {
		t.Fatalf("code %d view %+v", code, out)
	}
	p, _ := e.st.GetPeering(context.Background(), v.ID)
	if p.AcceptedBy != "ub" || p.AccepterRouterID != routerB {
		t.Fatalf("row %+v", p)
	}
}

func TestAcceptByRequester403(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	if code := e.accept(t, v.ID, "tok-a", routerA, nil); code != 403 {
		t.Fatalf("code %d", code)
	}
	if code := e.accept(t, v.ID, "tok-c", routerC, nil); code != 404 {
		t.Fatalf("foreign project: code %d", code)
	}
}

func TestAcceptRouterNotOwned400(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	if code := e.accept(t, v.ID, "tok-b", routerC, nil); code != 400 {
		t.Fatalf("code %d", code)
	}
}

func TestAcceptOverlap409(t *testing.T) {
	e := newTEnv(t)
	e.nf.AddRouterSubnet(routerB, netip.MustParsePrefix("10.1.0.0/24"))
	v := e.create(t)
	var out errorBody
	if code := e.accept(t, v.ID, "tok-b", routerB, &out); code != 409 || len(out.Overlaps) != 1 || out.Overlaps[0] != "10.1.0.0/24" {
		t.Fatalf("code %d body %+v", code, out)
	}
	if e.status(t, v.ID) != model.StatusPendingAcceptance {
		t.Fatal("must stay pending")
	}
}

func TestAcceptOverlapWithPeersOfPeer409(t *testing.T) {
	e := newTEnv(t)
	// A is already peered with C (10.3.0.0/24); B also uses 10.3.0.0/24.
	var ac View
	e.do(t, "POST", "/v1/peerings", "tok-a", map[string]string{"name": "a-c", "router_id": routerA, "peer_project_id": "pc"}, &ac)
	if code := e.accept(t, ac.ID, "tok-c", routerC, nil); code != 202 {
		t.Fatalf("setup accept %d", code)
	}
	e.nf.AddRouterSubnet(routerB, netip.MustParsePrefix("10.3.0.0/24"))
	v := e.create(t)
	var out errorBody
	if code := e.accept(t, v.ID, "tok-b", routerB, &out); code != 409 || len(out.Overlaps) != 1 || out.Overlaps[0] != "10.3.0.0/24" {
		t.Fatalf("code %d body %+v", code, out)
	}
}

func TestAcceptRequesterRouterGone(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	e.nf.DeleteRouter(routerA)
	var out errorBody
	if code := e.accept(t, v.ID, "tok-b", routerB, &out); code != 409 || out.Error != "requester router no longer exists" {
		t.Fatalf("code %d body %+v", code, out)
	}
}

func TestAcceptRequesterProjectDisabled409(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	e.kf.AddProject(keystone.Project{ID: "pa", Name: "team-a", Enabled: false})
	if code := e.accept(t, v.ID, "tok-b", routerB, nil); code != 409 {
		t.Fatalf("code %d", code)
	}
}

func TestAcceptDuplicatePair409(t *testing.T) {
	e := newTEnv(t)
	v1 := e.create(t)
	v2 := e.create(t)
	if code := e.accept(t, v1.ID, "tok-b", routerB, nil); code != 202 {
		t.Fatalf("first accept %d", code)
	}
	if code := e.accept(t, v2.ID, "tok-b", routerB, nil); code != 409 {
		t.Fatalf("second accept %d", code)
	}
}

func TestAcceptRacesCancel(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	var wg sync.WaitGroup
	codes := make([]int, 2)
	wg.Add(2)
	go func() { defer wg.Done(); codes[0] = e.accept(t, v.ID, "tok-b", routerB, nil) }()
	go func() { defer wg.Done(); codes[1] = e.do(t, "DELETE", "/v1/peerings/"+v.ID, "tok-a", nil, nil) }()
	wg.Wait()
	// Valid outcomes: the cancel won (accept 409, cancelled); the accept won and the cancel lost
	// (202, 409, provisioning); or the accept won and the DELETE, arriving after it, started a normal
	// delete (202, 202, deleting). Never both a cancel and an accept.
	switch st := e.status(t, v.ID); {
	case codes[1] == 200 && codes[0] == 409 && st == model.StatusCancelled:
	case codes[0] == 202 && codes[1] == 409 && st == model.StatusProvisioning:
	case codes[0] == 202 && codes[1] == 202 && st == model.StatusDeleting:
	default:
		t.Fatalf("inconsistent outcome: accept %d, delete %d, status %s", codes[0], codes[1], st)
	}
}

func TestReject(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	if code := e.do(t, "POST", "/v1/peerings/"+v.ID+"/reject", "tok-a", nil, nil); code != 403 {
		t.Fatalf("requester reject %d", code)
	}
	if code := e.do(t, "POST", "/v1/peerings/"+v.ID+"/reject", "tok-b", nil, nil); code != 200 || e.status(t, v.ID) != model.StatusRejected {
		t.Fatalf("reject %d", code)
	}
	if code := e.do(t, "POST", "/v1/peerings/"+v.ID+"/reject", "tok-b", nil, nil); code != 409 {
		t.Fatalf("second reject %d", code)
	}
}

func TestRepairOnlyDegraded(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	if code := e.do(t, "POST", "/v1/peerings/"+v.ID+"/repair", "tok-a", nil, nil); code != 409 {
		t.Fatalf("repair pending %d", code)
	}
	e.accept(t, v.ID, "tok-b", routerB, nil)
	e.setStatus(t, v.ID, model.StatusProvisioning, model.StatusActive)
	e.setStatus(t, v.ID, model.StatusActive, model.StatusDegraded)
	if code := e.do(t, "POST", "/v1/peerings/"+v.ID+"/repair", "tok-b", nil, nil); code != 202 || e.status(t, v.ID) != model.StatusProvisioning {
		t.Fatalf("repair degraded %d", code)
	}
	last, _ := e.st.LastAction(context.Background(), v.ID)
	if last != model.ActionRepairRequested {
		t.Fatalf("last %q", last)
	}
}

func TestDeleteMatrix(t *testing.T) {
	cases := []struct {
		name   string
		status model.Status
		token  string
		code   int
		want   model.Status
	}{
		{"requester cancels pending", model.StatusPendingAcceptance, "tok-a", 200, model.StatusCancelled},
		{"accepter cannot delete pending", model.StatusPendingAcceptance, "tok-b", 409, model.StatusPendingAcceptance},
		{"admin cancels pending", model.StatusPendingAcceptance, "tok-admin", 200, model.StatusCancelled},
		{"provisioning", model.StatusProvisioning, "tok-b", 202, model.StatusDeleting},
		{"active by requester", model.StatusActive, "tok-a", 202, model.StatusDeleting},
		{"active by admin", model.StatusActive, "tok-admin", 202, model.StatusDeleting},
		{"degraded", model.StatusDegraded, "tok-b", 202, model.StatusDeleting},
		{"deleting again", model.StatusDeleting, "tok-a", 202, model.StatusDeleting},
		{"terminal", model.StatusRejected, "tok-a", 409, model.StatusRejected},
		{"foreign", model.StatusActive, "tok-c", 404, model.StatusActive},
		{"reader", model.StatusActive, "tok-a-reader", 403, model.StatusActive},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTEnv(t)
			v := e.create(t)
			switch c.status {
			case model.StatusRejected:
				e.do(t, "POST", "/v1/peerings/"+v.ID+"/reject", "tok-b", nil, nil)
			case model.StatusPendingAcceptance:
			default:
				e.accept(t, v.ID, "tok-b", routerB, nil)
				path := []model.Status{model.StatusProvisioning, model.StatusActive, model.StatusDegraded}
				if c.status == model.StatusDeleting {
					e.setStatus(t, v.ID, model.StatusProvisioning, model.StatusDeleting)
				}
				for i := 1; i < len(path) && path[i-1] != c.status && c.status != model.StatusDeleting; i++ {
					e.setStatus(t, v.ID, path[i-1], path[i])
				}
			}
			if e.status(t, v.ID) != c.status {
				t.Fatalf("setup status %s", e.status(t, v.ID))
			}
			if code := e.do(t, "DELETE", "/v1/peerings/"+v.ID, c.token, nil, nil); code != c.code {
				t.Fatalf("code %d, want %d", code, c.code)
			}
			if got := e.status(t, v.ID); got != c.want {
				t.Fatalf("status %s, want %s", got, c.want)
			}
		})
	}
}

func TestEventsHideOtherSideActor(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	e.do(t, "POST", "/v1/peerings/"+v.ID+"/reject", "tok-b", nil, nil)
	var evs []map[string]any
	if code := e.do(t, "GET", "/v1/peerings/"+v.ID+"/events", "tok-a", nil, &evs); code != 200 || len(evs) != 2 {
		t.Fatalf("code %d events %v", code, evs)
	}
	if evs[0]["actor_user_id"] != "ua" {
		t.Fatalf("own event lost its actor: %v", evs[0])
	}
	if _, ok := evs[1]["actor_user_id"]; ok || evs[1]["action"] != model.ActionRejected {
		t.Fatalf("requester sees the accepter's user: %v", evs[1])
	}
	if _, ok := evs[1]["actor_project_id"]; ok {
		t.Fatalf("requester sees the accepter's actor project: %v", evs[1])
	}
	var admin []map[string]any
	e.do(t, "GET", "/v1/peerings/"+v.ID+"/events", "tok-admin", nil, &admin)
	if admin[1]["actor_user_id"] != "ub" {
		t.Fatalf("cloud admin must see every actor: %v", admin[1])
	}
}
