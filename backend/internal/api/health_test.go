package api

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestReadyz(t *testing.T) {
	e := newTEnv(t)
	h := New(e.st, e.kf, e.nf, e.cfg, WithServiceProject("pa"))
	get := func(path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Code
	}
	if code := get("/readyz"); code != 200 {
		t.Fatalf("ready: %d", code)
	}
	e.kf.FailGetProject = context.DeadlineExceeded
	if code := get("/readyz"); code != 503 {
		t.Fatalf("keystone down: %d", code)
	}
	if code := get("/healthz"); code != 200 {
		t.Fatalf("healthz must not depend on Keystone: %d", code)
	}
}
