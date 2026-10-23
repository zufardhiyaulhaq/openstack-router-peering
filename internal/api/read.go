package api

import (
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/store"
)

var errNotFound = store.ErrNotFound

// fullView adds the peer project name (accepter only) and the peer router's CIDRs. Lookup failures leave them empty.
func (a *API) fullView(r *http.Request, p model.Peering, c caller) View {
	project := c.ProjectID
	if c.side(p) == "" {
		project = p.RequesterProjectID // cloud admin: requester's perspective
	}
	var name string
	peerRouter := p.AccepterRouterID
	if project == p.AccepterProjectID {
		peerRouter = p.RequesterRouterID
		if pr, err := a.ks.GetProject(r.Context(), p.RequesterProjectID); err == nil {
			name = pr.Name
		}
	}
	var cidrs []netip.Prefix
	if peerRouter != "" && p.AccepterRouterID != "" {
		if rt, err := a.nc.GetRouter(r.Context(), peerRouter); err == nil {
			cidrs = rt.SubnetCIDRs
		}
	}
	return BuildView(p, project, name, cidrs)
}

func (a *API) list(w http.ResponseWriter, r *http.Request, c caller) {
	if !need(w, c, a.cfg.Roles.View) {
		return
	}
	q := r.URL.Query()
	f := store.ListFilter{ProjectID: c.ProjectID, Direction: q.Get("direction"), Marker: q.Get("marker"),
		All: c.CloudAdmin && q.Get("all_projects") == "true"}
	if f.Direction != "" && f.Direction != "incoming" && f.Direction != "outgoing" {
		writeErr(w, http.StatusBadRequest, "direction must be incoming or outgoing")
		return
	}
	for _, s := range q["status"] {
		f.Statuses = append(f.Statuses, model.Status(s))
	}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		f.Limit = n
	}
	ps, err := a.st.ListPeerings(r.Context(), f)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]View, 0, len(ps))
	for _, p := range ps {
		project := c.ProjectID
		if c.side(p) == "" {
			project = p.RequesterProjectID
		}
		out = append(out, BuildView(p, project, "", nil))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) get(w http.ResponseWriter, r *http.Request, c caller) {
	p, ok := a.load(w, r, c)
	if !ok || !need(w, c, a.cfg.Roles.View) {
		return
	}
	writeJSON(w, http.StatusOK, a.fullView(r, p, c))
}

type eventView struct {
	At             time.Time      `json:"at"`
	Action         string         `json:"action"`
	ActorUserID    string         `json:"actor_user_id,omitempty"`
	ActorProjectID string         `json:"actor_project_id,omitempty"`
	Detail         map[string]any `json:"detail,omitempty"`
}

func (a *API) events(w http.ResponseWriter, r *http.Request, c caller) {
	p, ok := a.load(w, r, c)
	if !ok || !need(w, c, a.cfg.Roles.View) {
		return
	}
	evs, err := a.st.ListEvents(r.Context(), p.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]eventView, 0, len(evs))
	for _, e := range evs {
		out = append(out, eventView{At: e.At, Action: e.Action, ActorUserID: e.ActorUserID, ActorProjectID: e.ActorProjectID, Detail: e.Detail})
	}
	writeJSON(w, http.StatusOK, out)
}
