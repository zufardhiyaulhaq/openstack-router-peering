package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/keystone"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/neutron"
)

type createReq struct {
	Name          string `json:"name"`
	RouterID      string `json:"router_id"`
	PeerProjectID string `json:"peer_project_id"`
	PeerRouterID  string `json:"peer_router_id"`
}

func (a *API) create(w http.ResponseWriter, r *http.Request, c caller) {
	if !need(w, c, a.cfg.Roles.Request) {
		return
	}
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with name, router_id, peer_project_id and peer_router_id")
		return
	}
	switch {
	case req.Name == "" || len(req.Name) > 255:
		writeErr(w, http.StatusBadRequest, "name is required (at most 255 characters)")
		return
	case !model.ValidProjectID(req.PeerProjectID):
		writeErr(w, http.StatusBadRequest, "peer_project_id is not a valid project ID")
		return
	case req.PeerProjectID == c.ProjectID:
		writeErr(w, http.StatusBadRequest, "cannot peer with your own project")
		return
	case req.RouterID == "":
		writeErr(w, http.StatusBadRequest, "router_id is required")
		return
	case req.PeerRouterID == "":
		writeErr(w, http.StatusBadRequest, "peer_router_id is required")
		return
	case !model.ValidRouterID(req.RouterID):
		writeErr(w, http.StatusBadRequest, "router_id is not a valid router ID")
		return
	case !model.ValidRouterID(req.PeerRouterID):
		writeErr(w, http.StatusBadRequest, "peer_router_id is not a valid router ID")
		return
	}
	if _, err := a.ks.GetProject(r.Context(), req.PeerProjectID); errors.Is(err, keystone.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "peer project not found")
		return
	} else if err != nil {
		slog.Error("keystone get project", "err", err)
		writeErr(w, http.StatusServiceUnavailable, "cannot reach Keystone")
		return
	}
	if ok := a.ownRouter(w, r, c, req.RouterID); !ok {
		return
	}
	// The peer router must exist in the peer project. Like the project ID, this reveals existence only.
	if rt, err := a.nc.GetRouter(r.Context(), req.PeerRouterID); neutron.KindOf(err) == neutron.KindNotFound ||
		(err == nil && rt.ProjectID != req.PeerProjectID) {
		writeErr(w, http.StatusNotFound, "router not found in peer project")
		return
	} else if err != nil {
		slog.Error("neutron get router", "err", err)
		writeErr(w, http.StatusServiceUnavailable, "cannot reach Neutron")
		return
	}
	n, err := a.st.CountNonTerminal(r.Context(), c.ProjectID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if n >= a.cfg.MaxNonTerminalPerProject {
		writeErr(w, http.StatusConflict, "project has reached its limit of open peerings")
		return
	}
	p := &model.Peering{Name: req.Name, RequesterProjectID: c.ProjectID, RequesterRouterID: req.RouterID,
		AccepterProjectID: req.PeerProjectID, AccepterRouterID: req.PeerRouterID, CreatedBy: c.UserID,
		ExpiresAt: time.Now().Add(a.cfg.PendingTTL)}
	if err := a.st.CreatePeering(r.Context(), p); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, BuildView(*p, c.ProjectID, ""))
}

// ownRouter writes 400 unless routerID exists and belongs to the caller's project.
func (a *API) ownRouter(w http.ResponseWriter, r *http.Request, c caller, routerID string) bool {
	rt, err := a.nc.GetRouter(r.Context(), routerID)
	if neutron.KindOf(err) == neutron.KindNotFound || (err == nil && rt.ProjectID != c.ProjectID) {
		writeErr(w, http.StatusBadRequest, "router not found in your project")
		return false
	}
	if err != nil {
		slog.Error("neutron get router", "err", err)
		writeErr(w, http.StatusServiceUnavailable, "cannot reach Neutron")
		return false
	}
	return true
}
