package neutron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
)

type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func newServer(t *testing.T, h http.HandlerFunc) *Neutron {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	sc := &gophercloud.ServiceClient{ProviderClient: &gophercloud.ProviderClient{TokenID: "tok"}, Endpoint: srv.URL + "/v2.0/"}
	n, err := NewNeutron(context.Background(), sc, "svc-project")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func extensions(withTagCreation bool) map[string]any {
	ext := []map[string]any{{"alias": "standard-attr-tag"}, {"alias": "router"}}
	if withTagCreation {
		ext = append(ext, map[string]any{"alias": "tag-creation"})
	}
	return map[string]any{"extensions": ext}
}

func TestKindOf(t *testing.T) {
	cases := []struct {
		err  error
		want Kind
	}{
		{&APIError{Code: 503}, KindTransient},
		{&APIError{Code: 404}, KindNotFound},
		{fmt.Errorf("x: %w", ErrNotFound), KindNotFound},
		{&APIError{Code: 409, Type: "RouterInterfaceInUseByRoute"}, KindInUseByRoute},
		{&APIError{Code: 409, Type: "IpAddressAlreadyAllocated"}, KindTransient},
		{&APIError{Code: 400, Type: "BadRequest"}, KindHard},
		{&APIError{Code: 403}, KindHard},
		{context.DeadlineExceeded, KindTransient},
		{errors.New("connection refused"), KindTransient},
	}
	for _, c := range cases {
		if got := KindOf(c.err); got != c.want {
			t.Errorf("KindOf(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestAPIErrorFromResponse(t *testing.T) {
	n := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2.0/extensions" {
			writeJSON(w, 200, extensions(true))
			return
		}
		writeJSON(w, 409, map[string]any{"NeutronError": map[string]any{"type": "RouterInterfaceInUseByRoute", "message": "Router interface for subnet x is required by routes"}})
	})
	err := n.RemoveRouterInterface(context.Background(), "r1", "p1")
	if KindOf(err) != KindInUseByRoute || !errors.Is(err, ErrInUseByRoute) || !strings.Contains(err.Error(), "required by routes") {
		t.Fatalf("got %v", err)
	}
}

func TestTagFallback(t *testing.T) {
	rec := &recorder{}
	n := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		rec.add(r.Method + " " + r.URL.Path)
		switch {
		case r.URL.Path == "/v2.0/extensions":
			writeJSON(w, 200, extensions(false))
		case r.Method == "POST" && r.URL.Path == "/v2.0/networks":
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "tags") {
				t.Errorf("tags sent without tag-creation: %s", body)
			}
			if !strings.Contains(string(body), `"port_security_enabled":false`) {
				t.Errorf("port security not disabled: %s", body)
			}
			writeJSON(w, 201, map[string]any{"network": map[string]any{"id": "net-1"}})
		case r.Method == "PUT":
			w.WriteHeader(201)
		}
	})
	id, err := n.CreateNetwork(context.Background(), "rpx-x", "rpx-x")
	if err != nil || id != "net-1" {
		t.Fatalf("id %q err %v", id, err)
	}
	want := []string{"GET /v2.0/extensions", "POST /v2.0/networks", "PUT /v2.0/networks/net-1/tags/rpx-x"}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("calls %v", rec.calls)
	}
}

func TestCreateSubnetBody(t *testing.T) {
	var body map[string]map[string]any
	n := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2.0/extensions" {
			writeJSON(w, 200, extensions(true))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, 201, map[string]any{"subnet": map[string]any{"id": "sub-1"}})
	})
	if _, err := n.CreateSubnet(context.Background(), "net-1", "rpx-x", "rpx-x", netip.MustParsePrefix("100.64.0.8/29")); err != nil {
		t.Fatal(err)
	}
	s := body["subnet"]
	if gw, ok := s["gateway_ip"]; !ok || gw != nil {
		t.Errorf("gateway_ip must be explicit null, got %v (present %v)", gw, ok)
	}
	if s["enable_dhcp"] != false || s["cidr"] != "100.64.0.8/29" || s["ip_version"] != float64(4) {
		t.Errorf("body %v", s)
	}
	if tags, _ := s["tags"].([]any); len(tags) != 1 || tags[0] != "rpx-x" {
		t.Errorf("tags %v", s["tags"])
	}
}

func TestGetRouterExcludesTransit(t *testing.T) {
	n := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2.0/extensions":
			writeJSON(w, 200, extensions(true))
		case "/v2.0/routers/r1":
			writeJSON(w, 200, map[string]any{"router": map[string]any{"id": "r1", "project_id": "pa"}})
		case "/v2.0/ports":
			if r.URL.Query().Get("device_id") != "r1" {
				t.Errorf("ports query %v", r.URL.RawQuery)
			}
			writeJSON(w, 200, map[string]any{"ports": []map[string]any{
				{"id": "p1", "device_owner": "network:router_interface", "fixed_ips": []map[string]any{{"subnet_id": "s1", "ip_address": "10.0.0.1"}}},
				{"id": "p2", "device_owner": "network:router_interface", "fixed_ips": []map[string]any{{"subnet_id": "s2", "ip_address": "100.64.0.9"}}},
				{"id": "p4", "device_owner": "network:router_interface", "fixed_ips": []map[string]any{{"subnet_id": "s4", "ip_address": "10.9.0.1"}}},
				{"id": "p3", "device_owner": "network:router_gateway", "fixed_ips": []map[string]any{{"subnet_id": "s3", "ip_address": "198.51.100.5"}}},
			}})
		case "/v2.0/subnets":
			writeJSON(w, 200, map[string]any{"subnets": []map[string]any{
				{"id": "s1", "name": "app", "cidr": "10.0.0.0/24", "ip_version": 4, "project_id": "pa"},
				{"id": "s2", "name": "rpx-abc", "cidr": "100.64.0.8/29", "ip_version": 4, "project_id": "svc-project"},
				{"id": "s4", "name": "rpx-sneaky", "cidr": "10.9.0.0/24", "ip_version": 4, "project_id": "pa"},
			}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	rt, err := n.GetRouter(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	// Transit subnets are recognised by owner (the service project), not by name: a tenant subnet named rpx-* still counts.
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("10.9.0.0/24")}
	if rt.ProjectID != "pa" || !reflect.DeepEqual(rt.SubnetCIDRs, want) {
		t.Fatalf("router %+v", rt)
	}
}

func TestGetRouterNotFound(t *testing.T) {
	n := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2.0/extensions" {
			writeJSON(w, 200, extensions(true))
			return
		}
		writeJSON(w, 404, map[string]any{"NeutronError": map[string]any{"type": "RouterNotFound", "message": "Router r9 could not be found"}})
	})
	if _, err := n.GetRouter(context.Background(), "r9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestLookupsScopedToServiceProject(t *testing.T) {
	seen := map[string]url.Values{}
	n := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2.0/extensions" {
			writeJSON(w, 200, extensions(true))
			return
		}
		seen[r.URL.Path] = r.URL.Query()
		switch r.URL.Path {
		case "/v2.0/networks":
			writeJSON(w, 200, map[string]any{"networks": []map[string]any{{"id": "n1", "name": "rpx-1"}}})
		case "/v2.0/subnets":
			writeJSON(w, 200, map[string]any{"subnets": []map[string]any{{"id": "s1", "name": "rpx-1", "cidr": "10.0.0.0/24", "ip_version": 4, "project_id": "pa"}}})
		case "/v2.0/ports":
			writeJSON(w, 200, map[string]any{"ports": []map[string]any{}})
		}
	})
	ctx := context.Background()
	_, _ = n.FindNetworkByName(ctx, "rpx-1")
	if q := seen["/v2.0/networks"]; q.Get("project_id") != "svc-project" || q.Get("name") != "rpx-1" {
		t.Errorf("network lookup query %v", q)
	}
	_, _ = n.FindSubnetByName(ctx, "rpx-1")
	if q := seen["/v2.0/subnets"]; q.Get("project_id") != "svc-project" {
		t.Errorf("subnet lookup query %v", q)
	}
	_, _ = n.ListNetworkPorts(ctx, "n1")
	if q := seen["/v2.0/ports"]; q.Get("network_id") != "n1" || q.Get("tags") != "" {
		t.Errorf("port listing query %v", q)
	}
	cidrs, _ := n.ListAllSubnetCIDRs(ctx)
	if len(cidrs) != 1 {
		t.Errorf("tenant subnet named rpx-* must count for the pool scan: %v", cidrs)
	}
}
