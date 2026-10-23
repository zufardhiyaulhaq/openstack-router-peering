package reconciler

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/keystone"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/neutron"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/store"
)

// ExpireOnce moves pending requests past expires_at to expired.
func (r *Reconciler) ExpireOnce(ctx context.Context) error {
	ps, err := r.Store.ListByStatus(ctx, model.StatusPendingAcceptance)
	if err != nil {
		return err
	}
	now := r.now()
	for _, p := range ps {
		if p.ExpiresAt.After(now) {
			continue
		}
		_, err := r.Store.Transition(ctx, p.ID, []model.Status{model.StatusPendingAcceptance}, model.StatusExpired, store.Patch{}, model.Event{Action: model.ActionExpired})
		if err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}

// goneProject returns the ID of a deleted project among ids, "" if all exist, or an error if Keystone failed.
func (r *Reconciler) goneProject(ctx context.Context, ids ...string) (string, error) {
	for _, id := range ids {
		_, err := r.Keystone.GetProject(ctx, id)
		if errors.Is(err, keystone.ErrNotFound) {
			return id, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", nil
}

// DriftOnce checks projects, router interfaces and CIDR overlap (spec §7.4, §7.5, §9).
func (r *Reconciler) DriftOnce(ctx context.Context) error {
	ps, err := r.Store.ListByStatus(ctx, model.StatusPendingAcceptance, model.StatusActive, model.StatusDegraded)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if err := r.driftOne(ctx, p); err != nil && !errors.Is(err, store.ErrConflict) {
			r.log().Warn("drift check", "id", p.ID, "err", err)
		}
	}
	return nil
}

func (r *Reconciler) driftOne(ctx context.Context, p model.Peering) error {
	gone, err := r.goneProject(ctx, p.RequesterProjectID, p.AccepterProjectID)
	if err != nil {
		return err // Keystone trouble: change nothing
	}
	if gone != "" {
		reason := fmt.Sprintf("project %s was deleted", gone)
		ev := model.Event{Action: model.ActionProjectGone, Detail: map[string]any{"project": gone}}
		if p.Status == model.StatusPendingAcceptance {
			_, err = r.Store.Transition(ctx, p.ID, []model.Status{p.Status}, model.StatusCancelled, store.Patch{StatusReason: &reason}, ev)
		} else {
			_, err = r.Store.Transition(ctx, p.ID, []model.Status{p.Status}, model.StatusDeleting, store.Patch{StatusReason: &reason}, ev)
		}
		return err
	}
	if p.Status == model.StatusPendingAcceptance {
		return nil
	}

	if p.Status == model.StatusActive {
		for _, s := range []struct{ port, router string }{{p.RequesterPortID, p.RequesterRouterID}, {p.AccepterPortID, p.AccepterRouterID}} {
			port, err := r.Neutron.GetPort(ctx, s.port)
			if err != nil && neutron.KindOf(err) != neutron.KindNotFound {
				return err
			}
			if err == nil && port.DeviceID == s.router {
				continue
			}
			reason := fmt.Sprintf("interface on router %s is missing", s.router)
			_, err = r.Store.Transition(ctx, p.ID, []model.Status{model.StatusActive}, model.StatusDegraded, store.Patch{StatusReason: &reason},
				model.Event{Action: model.ActionDegraded, Detail: map[string]any{"router": s.router}})
			return err
		}
	}

	a, err := r.Neutron.GetRouter(ctx, p.RequesterRouterID)
	if err != nil {
		return err
	}
	b, err := r.Neutron.GetRouter(ctx, p.AccepterRouterID)
	if err != nil {
		return err
	}
	var warnings []string
	for _, c := range model.Overlaps(a.SubnetCIDRs, b.SubnetCIDRs) {
		warnings = append(warnings, c.String()+" overlaps with the peer")
	}
	if len(warnings) == 0 && len(p.Warnings) == 0 || reflect.DeepEqual(warnings, p.Warnings) {
		return nil
	}
	return r.Store.SetWarnings(ctx, p.ID, warnings)
}

// SweepOnce deletes rpx-* networks of the service project that no live peering owns, once older than OrphanGrace.
func (r *Reconciler) SweepOnce(ctx context.Context) error {
	nets, err := r.Neutron.ListOwnNetworks(ctx)
	if err != nil {
		return err
	}
	now := r.now()
	for _, n := range nets {
		if !strings.HasPrefix(n.Name, neutron.TransitPrefix) || now.Sub(n.CreatedAt) < r.Cfg.OrphanGrace {
			continue
		}
		p, err := r.Store.GetPeering(ctx, n.Name)
		if err == nil && !p.Status.Terminal() {
			continue
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		// Only Neutron objects: the block of a terminal peering is already free.
		orphan := model.Peering{ID: n.Name, TransitNetworkID: n.ID}
		if err := r.removeNeutron(ctx, orphan); err != nil {
			r.log().Warn("orphan sweep", "network", n.ID, "err", err)
		}
	}
	return nil
}

// PoolScanOnce seeds the pool, frees blocks leaked by terminal peerings, and reserves blocks that overlap
// any existing subnet (spec §6.3).
func (r *Reconciler) PoolScanOnce(ctx context.Context) error {
	if _, err := r.Store.SeedBlocks(ctx, r.Cfg.TransitPool); err != nil {
		return err
	}
	if n, err := r.Store.FreeTerminalBlocks(ctx); err != nil {
		return err
	} else if n > 0 {
		r.log().Warn("freed transit blocks held by finished peerings", "count", n)
	}
	used, err := r.Neutron.ListAllSubnetCIDRs(ctx)
	if err != nil {
		return err
	}
	n, err := r.Store.SetReserved(ctx, used)
	if err == nil && n > 0 {
		r.log().Info("transit blocks reserved because they overlap existing subnets", "count", n)
	}
	return err
}
