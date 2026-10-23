package reconciler

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/config"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/keystone"
	kfake "github.com/zufardhiyaulhaq/openstack-router-peering/internal/keystone/fake"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
	nfake "github.com/zufardhiyaulhaq/openstack-router-peering/internal/neutron/fake"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/store"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/store/storetest"
)

const (
	routerA = "aaaaaaaa-0000-0000-0000-000000000001"
	routerB = "bbbbbbbb-0000-0000-0000-000000000001"
)

type env struct {
	st *store.Store
	nf *nfake.Fake
	kf *kfake.Fake
	r  *Reconciler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	nf := nfake.New()
	nf.AddRouter(routerA, "pa", netip.MustParsePrefix("10.1.0.0/24"))
	nf.AddRouter(routerB, "pb", netip.MustParsePrefix("10.2.0.0/24"))
	kf := kfake.New()
	kf.AddProject(keystone.Project{ID: "pa", Name: "team-a", Enabled: true})
	kf.AddProject(keystone.Project{ID: "pb", Name: "team-b", Enabled: true})
	cfg := config.Defaults()
	cfg.TransitPool = netip.MustParsePrefix("100.64.0.0/24")
	if _, err := st.SeedBlocks(context.Background(), cfg.TransitPool); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Store: st, Neutron: nf, Keystone: kf, Cfg: cfg, Me: "test-worker",
		// An hour in the past: retries are due at once, so tests need not wait for backoff.
		Now: func() time.Time { return time.Now().Add(-time.Hour) }}
	return &env{st: st, nf: nf, kf: kf, r: r}
}

// accepted creates a peering A→B and accepts it, leaving it in provisioning.
func (e *env) accepted(t *testing.T) model.Peering {
	t.Helper()
	ctx := context.Background()
	p := &model.Peering{Name: "p", RequesterProjectID: "pa", RequesterRouterID: routerA, AccepterProjectID: "pb", CreatedBy: "ua"}
	if err := e.st.CreatePeering(ctx, p); err != nil {
		t.Fatal(err)
	}
	rb := routerB
	got, err := e.st.Transition(ctx, p.ID, []model.Status{model.StatusPendingAcceptance}, model.StatusProvisioning,
		store.Patch{AccepterRouterID: &rb}, model.Event{Action: model.ActionAccepted})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// drain runs WorkOnce until the peering leaves provisioning/deleting or n rounds pass, and returns it.
func (e *env) drain(t *testing.T, id string, n int) model.Peering {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := e.r.WorkOnce(ctx); err != nil {
			t.Fatal(err)
		}
		p, err := e.st.GetPeering(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != model.StatusProvisioning && p.Status != model.StatusDeleting {
			return p
		}
	}
	p, _ := e.st.GetPeering(ctx, id)
	return p
}

func (e *env) get(t *testing.T, id string) model.Peering {
	t.Helper()
	p, err := e.st.GetPeering(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
