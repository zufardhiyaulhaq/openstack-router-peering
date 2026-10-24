package reconciler

import (
	"context"
	"errors"
	"net/netip"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/neutron"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/store"
)

// provision runs spec §7.2. Every step looks for what exists before creating, so it resumes after a crash
// and repairs a peering whose port was removed.
func (r *Reconciler) provision(ctx context.Context, p model.Peering) error {
	err := r.provisionSteps(ctx, &p)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrLeaseLost):
		return err
	case errors.Is(err, store.ErrPoolExhausted):
		return r.retry(ctx, p, "transit pool exhausted")
	case isHard(err):
		return r.failProvision(ctx, p, err)
	default:
		return r.retry(ctx, p, err.Error())
	}
}

func (r *Reconciler) provisionSteps(ctx context.Context, p *model.Peering) error {
	// 0. Both routers still exist and still belong to their projects.
	for _, s := range []struct{ router, project string }{{p.RequesterRouterID, p.RequesterProjectID}, {p.AccepterRouterID, p.AccepterProjectID}} {
		rt, err := r.Neutron.GetRouter(ctx, s.router)
		if neutron.KindOf(err) == neutron.KindNotFound {
			return hard("router %s no longer exists", s.router)
		}
		if err != nil {
			return err
		}
		if rt.ProjectID != s.project {
			return hard("router %s no longer belongs to project %s", s.router, s.project)
		}
	}

	// 1. Transit block.
	if !p.TransitCIDR.IsValid() {
		cidr, err := r.Store.AllocateBlock(ctx, p.ID, r.Me)
		if err != nil {
			return err
		}
		rip, aip := model.BlockIPs(cidr)
		if err := r.step(ctx, *p, "allocate", store.WorkerPatch{TransitCIDR: &cidr, RequesterIP: &rip, AccepterIP: &aip}); err != nil {
			return err
		}
		p.TransitCIDR, p.RequesterIP, p.AccepterIP = cidr, rip, aip
	}

	// 2. Network.
	if p.TransitNetworkID == "" {
		id, err := r.findOrCreateNetwork(ctx, p.ID)
		if err != nil {
			return err
		}
		if err := r.step(ctx, *p, "network", store.WorkerPatch{TransitNetworkID: &id}); err != nil {
			return err
		}
		p.TransitNetworkID = id
	}

	// 3. Subnet.
	if p.TransitSubnetID == "" {
		id, err := r.Neutron.FindSubnetByName(ctx, p.ID)
		if neutron.KindOf(err) == neutron.KindNotFound {
			id, err = r.Neutron.CreateSubnet(ctx, p.TransitNetworkID, p.ID, model.Tag(p.ID), p.TransitCIDR)
		}
		if err != nil {
			return err
		}
		if err := r.step(ctx, *p, "subnet", store.WorkerPatch{TransitSubnetID: &id}); err != nil {
			return err
		}
		p.TransitSubnetID = id
	}

	// 4-7. A port per side, attached to that side's router.
	if err := r.ensureSide(ctx, p, "requester", p.RequesterProjectID, p.RequesterRouterID, p.RequesterIP, &p.RequesterPortID); err != nil {
		return err
	}
	if err := r.ensureSide(ctx, p, "accepter", p.AccepterProjectID, p.AccepterRouterID, p.AccepterIP, &p.AccepterPortID); err != nil {
		return err
	}

	// 8. Done.
	action := model.ActionProvisioned
	if last, err := r.Store.LastAction(ctx, p.ID, model.ActionProvisionStep, model.ActionError); err == nil && last == model.ActionRepairRequested {
		action = model.ActionRepaired
	}
	return r.Store.Finish(ctx, p.ID, r.Me, model.StatusProvisioning, model.StatusActive, store.WorkerPatch{}, model.Event{Action: action})
}

func (r *Reconciler) findOrCreateNetwork(ctx context.Context, name string) (string, error) {
	n, err := r.Neutron.FindNetworkByName(ctx, name)
	if err == nil {
		return n.ID, nil
	}
	if neutron.KindOf(err) != neutron.KindNotFound {
		return "", err
	}
	return r.Neutron.CreateNetwork(ctx, name, model.Tag(name))
}

// ensureSide makes sure one side has its port at ip, attached to router.
func (r *Reconciler) ensureSide(ctx context.Context, p *model.Peering, name, project, router string, ip netip.Addr, portID *string) error {
	var port neutron.Port
	var err error
	if *portID != "" {
		port, err = r.Neutron.GetPort(ctx, *portID)
		if neutron.KindOf(err) == neutron.KindNotFound {
			*portID = "" // removed by hand: this is a repair
		} else if err != nil {
			return err
		}
	}
	if *portID == "" {
		port, err = r.Neutron.FindPortByIP(ctx, p.TransitNetworkID, ip)
		if neutron.KindOf(err) == neutron.KindNotFound {
			var id string
			id, err = r.Neutron.CreatePort(ctx, p.TransitNetworkID, p.TransitSubnetID, project, p.ID+"-"+name, model.Tag(p.ID), ip)
			if err != nil {
				return err
			}
			port, err = r.Neutron.GetPort(ctx, id)
		}
		if err != nil {
			return err
		}
		id := port.ID
		patch := store.WorkerPatch{RequesterPortID: &id}
		if name == "accepter" {
			patch = store.WorkerPatch{AccepterPortID: &id}
		}
		if err := r.step(ctx, *p, name+"-port", patch); err != nil {
			return err
		}
		*portID = id
	}
	if port.DeviceID == router {
		return nil
	}
	if err := r.Neutron.AddRouterInterface(ctx, router, port.ID); err != nil {
		return err
	}
	return r.step(ctx, *p, name+"-attach", store.WorkerPatch{})
}

// failProvision removes everything built so far, frees the block and marks the peering failed.
func (r *Reconciler) failProvision(ctx context.Context, p model.Peering, cause error) error {
	if err := r.removeAll(ctx, p); err != nil {
		if errors.Is(err, store.ErrLeaseLost) {
			return err
		}
		return r.retry(ctx, p, "rollback after "+cause.Error()+": "+err.Error())
	}
	reason := cause.Error()
	return r.Store.Finish(ctx, p.ID, r.Me, model.StatusProvisioning, model.StatusFailed, store.WorkerPatch{StatusReason: &reason},
		model.Event{Action: model.ActionError, Detail: map[string]any{"error": reason}})
}
