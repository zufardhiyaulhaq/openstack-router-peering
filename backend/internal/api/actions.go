package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"sort"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/keystone"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/neutron"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/store"
)

func (a *API) routeActions(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/peerings/{id}/accept", a.authed(a.accept))
	mux.HandleFunc("POST /v1/peerings/{id}/reject", a.authed(a.reject))
	mux.HandleFunc("POST /v1/peerings/{id}/repair", a.authed(a.repair))
	mux.HandleFunc("DELETE /v1/peerings/{id}", a.authed(a.delete))
}

func actor(c caller, action string) model.Event {
	return model.Event{Action: action, ActorUserID: c.UserID, ActorProjectID: c.ProjectID}
}

func (a *API) transition(w http.ResponseWriter, r *http.Request, c caller, p model.Peering, from []model.Status, to model.Status, patch store.Patch, action string, code int) {
	got, err := a.st.Transition(r.Context(), p.ID, from, to, patch, actor(c, action))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, code, a.fullView(r, got, c))
}

func conflict(w http.ResponseWriter, msg string) { writeErr(w, http.StatusConflict, msg) }

func (a *API) accept(w http.ResponseWriter, r *http.Request, c caller) {
	p, ok := a.load(w, r, c)
	if !ok || !need(w, c, a.cfg.Roles.Accept) {
		return
	}
	if c.side(p) != "accepter" {
		writeErr(w, http.StatusForbidden, "only the accepter project can accept")
		return
	}
	if p.Status != model.StatusPendingAcceptance {
		conflict(w, "peering is "+string(p.Status)+", not pending_acceptance")
		return
	}
	// No body: the requester named both routers at request time; the accepter only confirms.
	proj, err := a.ks.GetProject(r.Context(), p.RequesterProjectID)
	if errors.Is(err, keystone.ErrNotFound) || (err == nil && !proj.Enabled) {
		conflict(w, "requester project is deleted or disabled")
		return
	}
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "cannot reach Keystone")
		return
	}
	accRouter, err := a.nc.GetRouter(r.Context(), p.AccepterRouterID)
	if neutron.KindOf(err) == neutron.KindNotFound || (err == nil && accRouter.ProjectID != p.AccepterProjectID) {
		conflict(w, "your router no longer exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "cannot reach Neutron")
		return
	}
	reqRouter, err := a.nc.GetRouter(r.Context(), p.RequesterRouterID)
	if neutron.KindOf(err) == neutron.KindNotFound || (err == nil && reqRouter.ProjectID != p.RequesterProjectID) {
		conflict(w, "requester router no longer exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "cannot reach Neutron")
		return
	}
	overlaps, err := a.overlaps(r, p.ID, reqRouter, accRouter)
	if err != nil {
		slog.Error("overlap check", "err", err)
		writeErr(w, http.StatusServiceUnavailable, "cannot check CIDR overlap")
		return
	}
	if len(overlaps) > 0 {
		writeJSON(w, http.StatusConflict, errorBody{Error: "CIDRs overlap between the 2 sides; pick another router or change subnets", Overlaps: overlaps})
		return
	}
	by := c.UserID
	a.transition(w, r, c, p, []model.Status{model.StatusPendingAcceptance}, model.StatusProvisioning,
		store.Patch{AcceptedBy: &by}, model.ActionAccepted, http.StatusAccepted)
}

// overlaps applies spec §9: A vs B, A vs routers already peered with B, B vs routers already peered with A.
func (a *API) overlaps(r *http.Request, selfID string, ra, rb neutron.Router) ([]string, error) {
	peersOf := func(router string) ([]netip.Prefix, error) {
		ps, err := a.st.ListLiveByRouter(r.Context(), router)
		if err != nil {
			return nil, err
		}
		var out []netip.Prefix
		for _, p := range ps {
			if p.ID == selfID {
				continue
			}
			other := p.AccepterRouterID
			if other == router {
				other = p.RequesterRouterID
			}
			rt, err := a.nc.GetRouter(r.Context(), other)
			if neutron.KindOf(err) == neutron.KindNotFound {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, rt.SubnetCIDRs...)
		}
		return out, nil
	}
	peersOfB, err := peersOf(rb.ID)
	if err != nil {
		return nil, err
	}
	peersOfA, err := peersOf(ra.ID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, set := range [][]netip.Prefix{model.Overlaps(ra.SubnetCIDRs, rb.SubnetCIDRs), model.Overlaps(ra.SubnetCIDRs, peersOfB), model.Overlaps(rb.SubnetCIDRs, peersOfA)} {
		for _, c := range set {
			if !seen[c.String()] {
				seen[c.String()] = true
				out = append(out, c.String())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (a *API) reject(w http.ResponseWriter, r *http.Request, c caller) {
	p, ok := a.load(w, r, c)
	if !ok || !need(w, c, a.cfg.Roles.Reject) {
		return
	}
	if c.side(p) != "accepter" {
		writeErr(w, http.StatusForbidden, "only the accepter project can reject")
		return
	}
	a.transition(w, r, c, p, []model.Status{model.StatusPendingAcceptance}, model.StatusRejected, store.Patch{}, model.ActionRejected, http.StatusOK)
}

func (a *API) repair(w http.ResponseWriter, r *http.Request, c caller) {
	p, ok := a.load(w, r, c)
	if !ok || !need(w, c, a.cfg.Roles.Repair) {
		return
	}
	a.transition(w, r, c, p, []model.Status{model.StatusDegraded}, model.StatusProvisioning, store.Patch{ResetAttempts: true}, model.ActionRepairRequested, http.StatusAccepted)
}

func (a *API) delete(w http.ResponseWriter, r *http.Request, c caller) {
	p, ok := a.load(w, r, c)
	if !ok || !need(w, c, a.cfg.Roles.Delete) {
		return
	}
	by := c.UserID
	switch {
	case p.Status == model.StatusPendingAcceptance && (c.side(p) == "requester" || c.CloudAdmin):
		a.transition(w, r, c, p, []model.Status{model.StatusPendingAcceptance}, model.StatusCancelled, store.Patch{DeletedBy: &by}, model.ActionCancelled, http.StatusOK)
	case p.Status == model.StatusPendingAcceptance:
		conflict(w, "the accepter rejects a pending request instead of deleting it")
	case p.Status == model.StatusDeleting:
		writeJSON(w, http.StatusAccepted, a.fullView(r, p, c))
	case p.Status.Terminal():
		conflict(w, "peering is already "+string(p.Status))
	default:
		a.transition(w, r, c, p, []model.Status{model.StatusProvisioning, model.StatusActive, model.StatusDegraded}, model.StatusDeleting,
			store.Patch{DeletedBy: &by}, model.ActionDeleteRequested, http.StatusAccepted)
	}
}
