# Peer Router at Request Time Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship spec `2026-10-25-peer-router-at-request-design.md`: the requester names the peer router, accept only confirms, no peer CIDRs, batched subnet lookups, live-only lists with a History tab.

**Architecture:** Backend changes in `backend/internal/{api,reconciler}`; Horizon changes in `frontend/router_peering_dashboard`. No DB migration. Backend and plugin ship together.

**Tech Stack:** Go 1.25 + Postgres (backend), Django 4.2 / Horizon 25 in the Kolla 2026.1 image (plugin).

**Spec:** `docs/superpowers/specs/2026-10-25-peer-router-at-request-design.md` (decisions D1–D16).

## Global Constraints

- Already on branch `feat/peer-router`, built before the spec: `74c6446` (D9 batching) and `915e98a` (D1, D2, D3, D4, D5, D6, D7, D8, D10, e2e update). Task 0 checks them; later tasks add only what is missing.
- Error texts exactly as the spec quotes them.
- Backend tests: `make -C backend test-db`. Plugin tests: `make -C frontend test`.

## Review Focus

1. Request A→B while B→A is live → 409 (symmetric index). Covered by `TestCreateDuplicatePair409` (reverse case).
2. Accept body naming another router → 400, peering stays pending (D13). Task 1 `TestAcceptOtherRouterInBody400`.
3. Pending request, either router deleted → drift cancels it with a reason, and Accept is no longer offered (D12). Task 2 `TestDriftCancelsPendingRouterGone`.
4. Horizon History tab must never show live rows, and Outgoing/Incoming must never show finished ones (D16). Task 4 `test_tabs_split_live_and_history`.
5. Malformed router ID → 400 before any Neutron call (D14). Task 1 `TestCreateBadRouterID400`.

---

### Task 0: Check the early commits against the spec

- [ ] Read `git show 74c6446 915e98a`. For each of D1–D10, note "done" or the gap. Expected gaps: D13 (current code ignores the body), D11, D12, D14, D16, all of the plugin. Ledger any other gap and fix it inside the task that owns that code.

### Task 1: API — D11, D13, D14

**Files:** `backend/internal/api/{create,actions}.go`, `backend/internal/model/model.go`, tests in `backend/internal/api/{api,actions}_test.go`, `backend/internal/model/model_test.go`

**Interfaces:** `model.ValidRouterID(s string) bool` (loose: 36 chars, hex digits and dashes in 8-4-4-4-12 shape).

- [ ] Tests (RED): `TestValidRouterID`; `TestCreateBadRouterID400` (both fields; fake Neutron `FailNext("GetRouter", …)` proves no call happened); `TestAcceptOtherRouterInBody400` (body `{router_id: routerB2}` → 400 with "this peering is for router <routerB>; accept takes no router", status still pending); `TestAcceptSameRouterInBody202`; `TestAcceptLegacyRowWithoutRouter409` (row inserted with `AccepterRouterID` empty via store, accept → 409 "this request names no peer router; ask the requester to request again"). Replace `TestAcceptIgnoresRouterInBody` with the 2 body tests.
- [ ] Implement; `make -C backend test-db` → all pass.
- [ ] Commit `feat(api): validate router IDs, refuse a different router on accept, handle legacy rows`.

### Task 2: Reconciler — D12

**Files:** `backend/internal/reconciler/jobs.go`, `jobs_test.go`, `backend/internal/model/model.go` (action `router_gone`)

- [ ] Tests (RED): `TestDriftCancelsPendingRouterGone` (delete router B in the fake → pending peering `cancelled`, reason "router <id> no longer exists", last event `router_gone`); same for requester router; router moved to another project counts as gone; Neutron error → no change.
- [ ] Implement in `driftOne` for `pending_acceptance` rows (after the project check). Commit `feat(reconciler): cancel pending requests whose router is gone`.

### Task 3: Plugin — API client and views for the new contract (D1, D3, D7, D8, D15)

**Files:** `frontend/router_peering_dashboard/api/peering.py` (+ tests, already partly edited, uncommitted), `.../test/helpers.py`, `.../router_peering/{forms,views,tables}.py`, templates `_create.html`, `_accept.html`, `detail.html`, `tests.py`

**Interfaces:** `api.peering.create(request, name, router_id, peer_project_id, peer_router_id)`; `api.peering.accept(request, peering_id)` sends no body.

- [ ] Tests (RED), updating the existing ones:
  - create form posts `peer_router_id` and shows its help text;
  - accept form has **no router field** and shows "Your router: <name> (<id>)" from `neutron.router_get`, or the ID only if that call fails;
  - accept POST calls `accept(req, PID)`;
  - detail has no "Peer CIDRs", has "Peer router ID", and shows the placeholder hint (HTML-escaped `&lt;peer subnet&gt;`);
  - list rows show a Peer Router column.
- [ ] Implement; `make -C frontend test` → pass. Commit `feat(frontend): name the peer router at request, confirm-only accept, no peer CIDRs`.

### Task 4: Plugin — live-only tabs and History (D16)

**Files:** `api/peering.py` (`list(request, direction=None, statuses=None)` → repeated `status=` params), `tabs.py`, `tables.py` (`HistoryTable`, no row actions, column "Direction"), `tests.py`

- [ ] Tests (RED): `test_tabs_split_live_and_history` (Outgoing/Incoming request only live statuses; History requests the 5 finished ones with no direction; a finished row never appears in the live tabs); `test_history_has_no_actions`; API test for repeated `status=` params with paging.
- [ ] Implement; tests pass. Commit `feat(frontend): show live peerings by default, finished ones under History`.

### Task 5: Spec merge, review, rollout

- [ ] Update the base spec `2026-10-23-router-peering-design.md` §4.1/§4.2/§4.3/§8/§9 to point at the new decisions (one line each plus a link), so the 2 specs do not disagree.
- [ ] Final review (fresh reviewer) of `main..feat/peer-router`; fix Critical/Important with tests.
- [ ] Merge to `main`, push, wait for CI (`:sha-<new>`).
- [ ] the test cloud (each command shown first): set `ROUTER_PEERING_IMAGE` to the new tag, `docker compose up -d`; copy the plugin to `/opt/router-peering/dashboard/`, bump the `_9999` version line to `0.2.0`, `kolla-ansible reconfigure -i multinode --tags horizon`; check the panel route (302) and that the existing peering `rpx-0123456789ab` still shows `active`.
- [ ] Rebuild and rerun the e2e test on `.04` (expect 6/6 PASS); record it in `backend/test/e2e/README.md`.
