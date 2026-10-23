package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/keystone"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
)

// caller is the validated identity of a request.
type caller struct {
	keystone.Token
	CloudAdmin bool
}

// authed validates X-Auth-Token and requires a project-scoped token.
func (a *API) authed(h func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Auth-Token")
		if tok == "" {
			writeErr(w, http.StatusUnauthorized, "X-Auth-Token is required")
			return
		}
		t, err := a.ks.Validate(r.Context(), tok)
		if errors.Is(err, keystone.ErrUnauthorized) {
			writeErr(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if err != nil {
			slog.Error("keystone validate", "err", err)
			writeErr(w, http.StatusServiceUnavailable, "cannot reach Keystone")
			return
		}
		if t.ProjectID == "" {
			writeErr(w, http.StatusUnauthorized, "token must be scoped to a project")
			return
		}
		h(w, r, caller{Token: t, CloudAdmin: t.ProjectName == a.cfg.CloudAdminProject && t.HasRole("admin")})
	}
}

// side returns "requester", "accepter" or "" when the caller's project is on neither side.
func (c caller) side(p model.Peering) string {
	switch c.ProjectID {
	case p.RequesterProjectID:
		return "requester"
	case p.AccepterProjectID:
		return "accepter"
	}
	return ""
}

// load fetches a peering the caller may see: a side, or cloud admin. Anything else is 404.
func (a *API) load(w http.ResponseWriter, r *http.Request, c caller) (model.Peering, bool) {
	p, err := a.st.GetPeering(r.Context(), r.PathValue("id"))
	if err == nil && c.side(p) == "" && !c.CloudAdmin {
		err = errNotFound
	}
	if err != nil {
		writeStoreErr(w, err)
		return p, false
	}
	return p, true
}

// need writes 403 and returns false unless the caller holds role.
func need(w http.ResponseWriter, c caller, role string) bool {
	if c.HasRole(role) {
		return true
	}
	writeErr(w, http.StatusForbidden, "this action needs the "+role+" role")
	return false
}
