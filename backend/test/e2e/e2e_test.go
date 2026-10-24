//go:build e2e

// Package e2e runs the router peering flow against a real cloud. See README.md.
package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/config/clouds"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
)

// tenant is one project's clients and the resources the test built in it.
type tenant struct {
	name         string
	provider     *gophercloud.ProviderClient
	net, compute *gophercloud.ServiceClient
	projectID    string
	cidr, vmIP   string
	networkID    string
	subnetID     string
	routerID     string
	cleanup      []func()
}

func env(t *testing.T, key string) string {
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s not set", key)
	}
	return v
}

func login(t *testing.T, cloud, name, cidr, vmIP string) *tenant {
	t.Helper()
	ctx := context.Background()
	ao, eo, _, err := clouds.Parse(clouds.WithCloudName(cloud))
	if err != nil {
		t.Fatal(err)
	}
	ao.AllowReauth = true
	p, err := openstack.AuthenticatedClient(ctx, ao)
	if err != nil {
		t.Fatal(err)
	}
	proj, err := p.GetAuthResult().(tokens.CreateResult).ExtractProject()
	if err != nil || proj == nil {
		t.Fatal(err)
	}
	tn := &tenant{name: name, provider: p, projectID: proj.ID, cidr: cidr, vmIP: vmIP}
	if tn.net, err = openstack.NewNetworkV2(p, eo); err != nil {
		t.Fatal(err)
	}
	if tn.compute, err = openstack.NewComputeV2(p, eo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := len(tn.cleanup) - 1; i >= 0; i-- {
			tn.cleanup[i]()
		}
	})
	return tn
}

func check(t *testing.T, _ *http.Response, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func post(t *testing.T, sc *gophercloud.ServiceClient, url string, body, out any) {
	t.Helper()
	resp, err := sc.Post(context.Background(), url, body, out, &gophercloud.RequestOpts{OkCodes: []int{200, 201, 202}})
	check(t, resp, err)
}

func put(t *testing.T, sc *gophercloud.ServiceClient, url string, body any) {
	t.Helper()
	resp, err := sc.Put(context.Background(), url, body, nil, &gophercloud.RequestOpts{OkCodes: []int{200, 201, 202, 204}})
	check(t, resp, err)
}

func del(sc *gophercloud.ServiceClient, url string) func() {
	return func() {
		_, _ = sc.Delete(context.Background(), url, &gophercloud.RequestOpts{OkCodes: []int{202, 204, 404}})
	}
}

// buildNetwork creates network, subnet and router with an interface, all cleaned up after the test.
func (tn *tenant) buildNetwork(t *testing.T) {
	var n struct {
		Network struct{ ID string } `json:"network"`
	}
	post(t, tn.net, tn.net.ServiceURL("networks"), map[string]any{"network": map[string]any{"name": "rp-e2e-" + tn.name}}, &n)
	tn.networkID = n.Network.ID
	tn.cleanup = append(tn.cleanup, del(tn.net, tn.net.ServiceURL("networks", tn.networkID)))
	var s struct {
		Subnet struct{ ID string } `json:"subnet"`
	}
	post(t, tn.net, tn.net.ServiceURL("subnets"), map[string]any{"subnet": map[string]any{"network_id": tn.networkID, "cidr": tn.cidr, "ip_version": 4, "name": "rp-e2e-" + tn.name}}, &s)
	tn.subnetID = s.Subnet.ID
	var r struct {
		Router struct{ ID string } `json:"router"`
	}
	post(t, tn.net, tn.net.ServiceURL("routers"), map[string]any{"router": map[string]any{"name": "rp-e2e-" + tn.name}}, &r)
	tn.routerID = r.Router.ID
	tn.cleanup = append(tn.cleanup, del(tn.net, tn.net.ServiceURL("routers", tn.routerID)))
	put(t, tn.net, tn.net.ServiceURL("routers", tn.routerID, "add_router_interface"), map[string]string{"subnet_id": tn.subnetID})
	tn.cleanup = append(tn.cleanup, func() {
		_, _ = tn.net.Put(context.Background(), tn.net.ServiceURL("routers", tn.routerID, "remove_router_interface"),
			map[string]string{"subnet_id": tn.subnetID}, nil, &gophercloud.RequestOpts{OkCodes: []int{200, 404}})
	})
}

// boot starts a VM on a pre-made port with fixed IP vmIP; userData may be empty.
func (tn *tenant) boot(t *testing.T, image, flavor, sg, userData string) string {
	var port struct {
		Port struct{ ID string } `json:"port"`
	}
	body := map[string]any{"network_id": tn.networkID, "fixed_ips": []map[string]string{{"subnet_id": tn.subnetID, "ip_address": tn.vmIP}}}
	if sg != "" {
		body["security_groups"] = []string{sg}
	}
	post(t, tn.net, tn.net.ServiceURL("ports"), map[string]any{"port": body}, &port)
	tn.cleanup = append(tn.cleanup, del(tn.net, tn.net.ServiceURL("ports", port.Port.ID)))
	server := map[string]any{"name": "rp-e2e-" + tn.name, "imageRef": image, "flavorRef": flavor, "networks": []map[string]string{{"port": port.Port.ID}}}
	if userData != "" {
		server["user_data"] = base64.StdEncoding.EncodeToString([]byte(userData))
	}
	var s struct {
		Server struct{ ID string } `json:"server"`
	}
	post(t, tn.compute, tn.compute.ServiceURL("servers"), map[string]any{"server": server}, &s)
	tn.cleanup = append(tn.cleanup, del(tn.compute, tn.compute.ServiceURL("servers", s.Server.ID)))
	return s.Server.ID
}

func (tn *tenant) setRoutes(t *testing.T, routes []map[string]string) {
	if routes == nil {
		routes = []map[string]string{}
	}
	put(t, tn.net, tn.net.ServiceURL("routers", tn.routerID), map[string]any{"router": map[string]any{"routes": routes}})
}

func (tn *tenant) consoleLog(t *testing.T, server string) string {
	var out struct {
		Output string `json:"output"`
	}
	post(t, tn.compute, tn.compute.ServiceURL("servers", server, "action"), map[string]any{"os-getConsoleOutput": map[string]any{}}, &out)
	return out.Output
}

// view is the subset of the API's peering JSON the test reads.
type view struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	StatusReason string `json:"status_reason"`
	NextHop      string `json:"next_hop"`
}

type apiClient struct{ base string }

func (a apiClient) call(t *testing.T, tn *tenant, method, path string, body any, out any) int {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, a.base+path, &buf)
	req.Header.Set("X-Auth-Token", tn.provider.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (a apiClient) waitStatus(t *testing.T, tn *tenant, id, want string, timeout time.Duration) view {
	t.Helper()
	var v view
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(5 * time.Second) {
		a.call(t, tn, "GET", "/v1/peerings/"+id, nil, &v)
		if v.Status == want {
			return v
		}
	}
	t.Fatalf("peering %s: status %q (%s), want %q after %v", id, v.Status, v.StatusReason, want, timeout)
	return v
}

func (a apiClient) request(t *testing.T, ta, tb *tenant) view {
	var v view
	if code := a.call(t, ta, "POST", "/v1/peerings", map[string]string{"name": "e2e", "router_id": ta.routerID, "peer_project_id": tb.projectID}, &v); code != 201 {
		t.Fatalf("create: %d", code)
	}
	return v
}

func TestE2E(t *testing.T) {
	api := apiClient{base: strings.TrimRight(env(t, "RP_E2E_API_URL"), "/")}
	image, flavor := env(t, "RP_E2E_IMAGE"), env(t, "RP_E2E_FLAVOR")
	ta := login(t, env(t, "RP_E2E_CLOUD_A"), "a", "10.91.0.0/24", "10.91.0.10")
	tb := login(t, env(t, "RP_E2E_CLOUD_B"), "b", "10.92.0.0/24", "10.92.0.10")
	ta.buildNetwork(t)
	tb.buildNetwork(t)

	// B's VM accepts ICMP from A's subnet; A's VM pings it from cloud-init and prints the result.
	var sg struct {
		SecurityGroup struct{ ID string } `json:"security_group"`
	}
	post(t, tb.net, tb.net.ServiceURL("security-groups"), map[string]any{"security_group": map[string]any{"name": "rp-e2e"}}, &sg)
	tb.cleanup = append(tb.cleanup, del(tb.net, tb.net.ServiceURL("security-groups", sg.SecurityGroup.ID)))
	post(t, tb.net, tb.net.ServiceURL("security-group-rules"), map[string]any{"security_group_rule": map[string]any{
		"security_group_id": sg.SecurityGroup.ID, "direction": "ingress", "protocol": "icmp", "remote_ip_prefix": ta.cidr}}, nil)
	tb.boot(t, image, flavor, sg.SecurityGroup.ID, "")
	vmA := ta.boot(t, image, flavor, "", fmt.Sprintf("#!/bin/sh\nfor i in $(seq 120); do if ping -c1 -W1 %s >/dev/null; then echo PEERING-PING-OK > /dev/console; break; fi; sleep 5; done\n", tb.vmIP))

	var active view
	t.Run("create and accept", func(t *testing.T) {
		v := api.request(t, ta, tb)
		if code := api.call(t, tb, "POST", "/v1/peerings/"+v.ID+"/accept", map[string]string{"router_id": tb.routerID}, nil); code != 202 {
			t.Fatalf("accept: %d", code)
		}
		active = api.waitStatus(t, ta, v.ID, "active", 2*time.Minute)
	})
	var hopB view
	api.call(t, tb, "GET", "/v1/peerings/"+active.ID, nil, &hopB)

	t.Run("ping through the peering", func(t *testing.T) {
		ta.setRoutes(t, []map[string]string{{"destination": tb.cidr, "nexthop": active.NextHop}})
		tb.setRoutes(t, []map[string]string{{"destination": ta.cidr, "nexthop": hopB.NextHop}})
		for end := time.Now().Add(10 * time.Minute); time.Now().Before(end); time.Sleep(15 * time.Second) {
			if strings.Contains(ta.consoleLog(t, vmA), "PEERING-PING-OK") {
				return
			}
		}
		t.Fatal("A's VM never reached B's VM")
	})

	t.Run("delete blocked by route, then succeeds", func(t *testing.T) {
		if code := api.call(t, ta, "DELETE", "/v1/peerings/"+active.ID, nil, nil); code != 202 {
			t.Fatalf("delete: %d", code)
		}
		time.Sleep(30 * time.Second)
		v := api.waitStatus(t, ta, active.ID, "deleting", time.Second)
		if !strings.Contains(v.StatusReason, "remove routes") {
			t.Fatalf("reason %q", v.StatusReason)
		}
		ta.setRoutes(t, nil)
		tb.setRoutes(t, nil)
		api.waitStatus(t, ta, active.ID, "deleted", 5*time.Minute)
	})

	t.Run("cancel", func(t *testing.T) {
		v := api.request(t, ta, tb)
		if code := api.call(t, ta, "DELETE", "/v1/peerings/"+v.ID, nil, &v); code != 200 || v.Status != "cancelled" {
			t.Fatalf("cancel: %d %s", code, v.Status)
		}
	})

	t.Run("reject", func(t *testing.T) {
		v := api.request(t, ta, tb)
		if code := api.call(t, tb, "POST", "/v1/peerings/"+v.ID+"/reject", nil, &v); code != 200 || v.Status != "rejected" {
			t.Fatalf("reject: %d %s", code, v.Status)
		}
	})

	t.Run("degraded after manual detach, then repair", func(t *testing.T) {
		v := api.request(t, ta, tb)
		api.call(t, tb, "POST", "/v1/peerings/"+v.ID+"/accept", map[string]string{"router_id": tb.routerID}, nil)
		api.waitStatus(t, ta, v.ID, "active", 2*time.Minute)
		var ports struct {
			Ports []struct{ ID string } `json:"ports"`
		}
		resp, err := ta.net.Get(context.Background(), ta.net.ServiceURL("ports")+"?name="+v.ID+"-requester", &ports, nil)
		check(t, resp, err)
		if len(ports.Ports) != 1 {
			t.Fatalf("A cannot see its interface port: %v", ports)
		}
		put(t, ta.net, ta.net.ServiceURL("routers", ta.routerID, "remove_router_interface"), map[string]string{"port_id": ports.Ports[0].ID})
		api.waitStatus(t, ta, v.ID, "degraded", 7*time.Minute) // drift runs every 5 min by default
		if code := api.call(t, ta, "POST", "/v1/peerings/"+v.ID+"/repair", nil, nil); code != 202 {
			t.Fatalf("repair: %d", code)
		}
		api.waitStatus(t, ta, v.ID, "active", 2*time.Minute)
		api.call(t, tb, "DELETE", "/v1/peerings/"+v.ID, nil, nil)
		api.waitStatus(t, ta, v.ID, "deleted", 2*time.Minute)
	})
}
