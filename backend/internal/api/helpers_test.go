package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/config"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/keystone"
	kfake "github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/keystone/fake"
	nfake "github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/neutron/fake"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/store"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/store/storetest"
)

const (
	routerA  = "aaaaaaaa-0000-0000-0000-000000000001"
	routerB  = "bbbbbbbb-0000-0000-0000-000000000001"
	routerC  = "cccccccc-0000-0000-0000-000000000001"
	routerA2 = "aaaaaaaa-0000-0000-0000-000000000002"
	routerB2 = "bbbbbbbb-0000-0000-0000-000000000002"
)

type tenv struct {
	st  *store.Store
	kf  *kfake.Fake
	nf  *nfake.Fake
	cfg config.Config
	h   http.Handler
}

func newTEnv(t *testing.T) *tenv {
	t.Helper()
	st := storetest.New(t)
	kf := kfake.New()
	for _, p := range []keystone.Project{{ID: "pa", Name: "team-a", Enabled: true}, {ID: "pb", Name: "team-b", Enabled: true}, {ID: "pc", Name: "team-c", Enabled: true}, {ID: "padmin", Name: "admin", Enabled: true}} {
		kf.AddProject(p)
	}
	member := []string{"member", "reader"}
	kf.AddToken("tok-a", keystone.Token{UserID: "ua", ProjectID: "pa", ProjectName: "team-a", Roles: member})
	kf.AddToken("tok-a-reader", keystone.Token{UserID: "ua2", ProjectID: "pa", ProjectName: "team-a", Roles: []string{"reader"}})
	kf.AddToken("tok-b", keystone.Token{UserID: "ub", ProjectID: "pb", ProjectName: "team-b", Roles: member})
	kf.AddToken("tok-c", keystone.Token{UserID: "uc", ProjectID: "pc", ProjectName: "team-c", Roles: member})
	kf.AddToken("tok-admin", keystone.Token{UserID: "uadmin", ProjectID: "padmin", ProjectName: "admin", Roles: []string{"admin", "member", "reader"}})
	kf.AddToken("tok-unscoped", keystone.Token{UserID: "ua"})
	nf := nfake.New()
	nf.AddRouter(routerA, "pa", netip.MustParsePrefix("10.1.0.0/24"))
	nf.AddRouter(routerB, "pb", netip.MustParsePrefix("10.2.0.0/24"))
	nf.AddRouter(routerC, "pc", netip.MustParsePrefix("10.3.0.0/24"))
	nf.AddRouter(routerA2, "pa", netip.MustParsePrefix("10.5.0.0/24"))
	nf.AddRouter(routerB2, "pb", netip.MustParsePrefix("10.4.0.0/24"))
	cfg := config.Defaults()
	cfg.TransitPool = netip.MustParsePrefix("100.64.0.0/24")
	_, _ = st.SeedBlocks(context.Background(), cfg.TransitPool)
	return &tenv{st: st, kf: kf, nf: nf, cfg: cfg, h: New(st, kf, nf, cfg)}
}

// do sends a request and decodes a JSON object (or array under key "items" for lists) into out if non-nil.
func (e *tenv) do(t *testing.T, method, path, token string, body any, out any) int {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("X-Auth-Token", token)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return rec.Code
}

// create makes a peering from A's router to B's router and returns its view.
func (e *tenv) create(t *testing.T) View {
	t.Helper()
	return e.createPair(t, "tok-a", routerA, "pb", routerB)
}

func (e *tenv) createPair(t *testing.T, token, router, peerProject, peerRouter string) View {
	t.Helper()
	var v View
	body := map[string]string{"name": "pair", "router_id": router, "peer_project_id": peerProject, "peer_router_id": peerRouter}
	if code := e.do(t, "POST", "/v1/peerings", token, body, &v); code != 201 {
		t.Fatalf("create %s->%s: %d", router, peerRouter, code)
	}
	return v
}
