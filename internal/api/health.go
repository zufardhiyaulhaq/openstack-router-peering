package api

import (
	"context"
	"net/http"
	"time"
)

func (a *API) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is 200 only when Postgres and (if configured) Keystone answer.
func (a *API) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := a.st.Ping(ctx); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database: "+err.Error())
		return
	}
	if a.serviceProjectID != "" {
		if _, err := a.ks.GetProject(ctx, a.serviceProjectID); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "keystone: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
