package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/config"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
)

func TestCreate201(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	if !strings.HasPrefix(v.ID, "rpx-") || v.Status != "pending_acceptance" || v.Role != "requester" || v.PeerProjectID != "pb" || v.RouterID != routerA {
		t.Fatalf("view %+v", v)
	}
	p, err := e.st.GetPeering(context.Background(), v.ID)
	if err != nil || p.CreatedBy != "ua" || p.ExpiresAt.Before(time.Now().Add(167*time.Hour)) {
		t.Fatalf("row %+v err %v", p, err)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newTEnv(t)
	cases := []struct {
		name  string
		token string
		body  map[string]string
		code  int
	}{
		{"same project", "tok-a", map[string]string{"name": "x", "router_id": routerA, "peer_project_id": "pa"}, 400},
		{"bad project id", "tok-a", map[string]string{"name": "x", "router_id": routerA, "peer_project_id": "a b"}, 400},
		{"unknown peer", "tok-a", map[string]string{"name": "x", "router_id": routerA, "peer_project_id": "nope"}, 404},
		{"router not owned", "tok-a", map[string]string{"name": "x", "router_id": routerB, "peer_project_id": "pc"}, 400},
		{"router missing", "tok-a", map[string]string{"name": "x", "router_id": "dddddddd-0000-0000-0000-000000000001", "peer_project_id": "pb"}, 400},
		{"no name", "tok-a", map[string]string{"router_id": routerA, "peer_project_id": "pb"}, 400},
		{"reader role", "tok-a-reader", map[string]string{"name": "x", "router_id": routerA, "peer_project_id": "pb"}, 403},
	}
	for _, c := range cases {
		var out map[string]any
		if code := e.do(t, "POST", "/v1/peerings", c.token, c.body, &out); code != c.code || out["error"] == "" {
			t.Errorf("%s: code %d body %v, want %d", c.name, code, out, c.code)
		}
	}
}

func TestCreateLimit409(t *testing.T) {
	e := newTEnv(t)
	e.h = New(e.st, e.kf, e.nf, func() config.Config { c := e.cfg; c.MaxNonTerminalPerProject = 2; return c }())
	e.create(t)
	e.create(t)
	if code := e.do(t, "POST", "/v1/peerings", "tok-a", map[string]string{"name": "x", "router_id": routerA, "peer_project_id": "pb"}, nil); code != 409 {
		t.Fatalf("code %d", code)
	}
}

func TestUnscopedToken401(t *testing.T) {
	e := newTEnv(t)
	for _, tok := range []string{"", "tok-unscoped", "garbage"} {
		if code := e.do(t, "GET", "/v1/peerings", tok, nil, nil); code != 401 {
			t.Errorf("token %q: code %d", tok, code)
		}
	}
}

func TestForeignProjectGets404(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	for _, path := range []string{"/v1/peerings/" + v.ID, "/v1/peerings/" + v.ID + "/events"} {
		if code := e.do(t, "GET", path, "tok-c", nil, nil); code != 404 {
			t.Errorf("%s: code %d", path, code)
		}
	}
	if code := e.do(t, "GET", "/v1/peerings/rpx-missing", "tok-a", nil, nil); code != 404 {
		t.Errorf("missing: code %d", code)
	}
}

func TestGetPerCaller(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	var b View
	if code := e.do(t, "GET", "/v1/peerings/"+v.ID, "tok-b", nil, &b); code != 200 || b.Role != "accepter" || b.PeerProjectName != "team-a" || b.PeerProjectID != "pa" {
		t.Fatalf("code %d view %+v", code, b)
	}
	var a View
	if code := e.do(t, "GET", "/v1/peerings/"+v.ID, "tok-a-reader", nil, &a); code != 200 || a.PeerProjectName != "" {
		t.Fatalf("requester must not see the peer's name before accept: %d %+v", code, a)
	}
	var evs []map[string]any
	if code := e.do(t, "GET", "/v1/peerings/"+v.ID+"/events", "tok-b", nil, &evs); code != 200 || len(evs) != 1 || evs[0]["action"] != model.ActionRequested {
		t.Fatalf("events %d %v", code, evs)
	}
}

func TestListIncomingOutgoing(t *testing.T) {
	e := newTEnv(t)
	e.create(t)
	count := func(tok, q string) int {
		var out []View
		if code := e.do(t, "GET", "/v1/peerings"+q, tok, nil, &out); code != 200 {
			t.Fatalf("%s%s: code %d", tok, q, code)
		}
		return len(out)
	}
	if count("tok-a", "?direction=outgoing") != 1 || count("tok-a", "?direction=incoming") != 0 || count("tok-b", "?direction=incoming") != 1 ||
		count("tok-b", "") != 1 || count("tok-c", "") != 0 || count("tok-a", "?status=active") != 0 || count("tok-a", "?status=pending_acceptance") != 1 {
		t.Fatal("list filters wrong")
	}
	if code := e.do(t, "GET", "/v1/peerings?direction=sideways", "tok-a", nil, nil); code != 400 {
		t.Fatalf("bad direction: %d", code)
	}
}

func TestCloudAdminListsAll(t *testing.T) {
	e := newTEnv(t)
	v := e.create(t)
	var out []View
	if code := e.do(t, "GET", "/v1/peerings?all_projects=true", "tok-admin", nil, &out); code != 200 || len(out) != 1 {
		t.Fatalf("admin list %d %d", code, len(out))
	}
	if code := e.do(t, "GET", "/v1/peerings?all_projects=true", "tok-c", nil, &out); code != 200 || len(out) != 0 {
		t.Fatalf("non-admin all_projects must be ignored: %d %d", code, len(out))
	}
	if code := e.do(t, "GET", "/v1/peerings/"+v.ID, "tok-admin", nil, nil); code != 200 {
		t.Fatalf("admin get %d", code)
	}
}
