# Router Peering Horizon Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Horizon plugin, `router-peering-dashboard`, that adds **Project → Network → Router Peering** and drives the router-peering API for create, accept, reject, repair, delete and detail.

**Architecture:** A classic server-rendered Horizon panel (Django tables, tabs and modal forms), not AngularJS, so it ships almost no static files. A thin API module calls the router-peering service with the user's Keystone token from the Horizon session and finds the URL in the service catalog. Router dropdowns use Horizon's own `openstack_dashboard.api.neutron.router_list`. Tests run inside the exact Kolla Horizon 2026.1 image, so they match production.

**Tech Stack:** Python 3.12, Django 4.2, Horizon 25.x (OpenStack 2026.1), `requests`. Test image `quay.io/openstack.kolla/horizon:2026.1-ubuntu-noble` (linux/amd64).

**Spec:** `docs/superpowers/specs/2026-10-23-router-peering-design.md` (§4.2 per-caller view, §4.3 panel, §8 API).

## Global Constraints

- Code lives in `frontend/`: package `router_peering_dashboard`, distribution name `router-peering-dashboard`, Apache-2.0.
- Panel: `PANEL = 'router_peering'`, `PANEL_DASHBOARD = 'project'`, `PANEL_GROUP = 'network'`, enabled file `_1490_project_router_peering_panel.py`. The panel is shown only when the catalog has service type `router-peering` (`permissions = ('openstack.services.router-peering',)`).
- API base URL: `openstack_dashboard.api.base.url_for(request, 'router-peering')`. It honours `OPENSTACK_ENDPOINT_TYPE`. TLS verify follows Horizon's `OPENSTACK_SSL_NO_VERIFY` / `OPENSTACK_SSL_CACERT`. Every call sends `X-Auth-Token: request.user.token.id` and has a 10 s timeout.
- No AngularJS and no custom JS. Static files only if a template truly needs them. Copy-to-clipboard uses a plain `<pre>`.
- User-facing text is plain English and matches the API's field names: requester, accepter, next hop, peer project ID.
- Tests: `make -C frontend test` runs Django's test runner inside the Kolla image, with `frontend/` mounted. No network access is needed.

## Review Focus

1. **API down, or returns 5xx or a timeout** → the index page still renders with an error message and empty tables, never a Django 500. (Task 3 `test_index_api_error`.)
2. **409 on accept with `overlaps`** → the modal shows the API's message plus the overlapping CIDRs, and the request stays pending. (Task 5 `test_accept_overlap_message`.)
3. **The router-peering service is missing from the catalog** → the panel is hidden, and the URL gives 403 or a redirect, not a 500. (Task 1 `test_panel_hidden_without_service`.)
4. **Row actions offered by status and side** → Cancel only for a requester on pending; Accept and Reject only for an accepter on pending; Repair only on degraded; Delete on provisioning, active or degraded. Showing a wrong action gives the user a confusing 409. (Task 3 `test_row_actions_by_status`.)
5. **A token scoped to a project with no routers** → the Create and Accept forms show "no routers in this project" instead of an empty dropdown that submits nothing. (Task 4 `test_create_no_routers`.)

---

### Task 1: Package skeleton, test harness, panel registration

**Files:**
- Create: `frontend/pyproject.toml`, `frontend/Makefile`, `frontend/router_peering_dashboard/__init__.py`, `.../enabled/_1490_project_router_peering_panel.py`, `.../dashboards/__init__.py`, `.../dashboards/project/__init__.py`, `.../dashboards/project/router_peering/{__init__,panel,urls,views}.py`, `.../templates/router_peering/index.html`, `.../test/{__init__,settings,helpers}.py`, `.../dashboards/project/router_peering/tests.py`
- Modify: `frontend/README.md`

**Interfaces:**
- Produces: `RouterPeering(horizon.Panel)` with `name = _("Router Peering")`, `slug = 'router_peering'`, `permissions = ('openstack.services.router-peering',)`. URL names `horizon:project:router_peering:index` and, later, `:create`, `:accept`, `:detail`. Test base `router_peering_dashboard.test.helpers.TestCase(openstack_dashboard.test.helpers.TestCase)` with fixture helpers `peering(**over) -> dict` returning an API-shaped peering view (spec §4.2 JSON keys).
- `make -C frontend test` = `docker run --rm --platform linux/amd64 -v $PWD:/src -w /src --entrypoint /var/lib/kolla/venv/bin/python quay.io/openstack.kolla/horizon:2026.1-ubuntu-noble -m django test router_peering_dashboard --settings=router_peering_dashboard.test.settings`. `test/settings.py` imports `openstack_dashboard.test.settings` and appends the plugin to `INSTALLED_APPS` via the enabled file (Horizon's `update_dashboards`), like upstream plugins do.

- [ ] **Step 1:** Write `tests.py` with `test_index_renders` (GET index with `api.peering.list` mocked to `[]` → 200, template `router_peering/index.html`) and `test_panel_hidden_without_service` (catalog without `router-peering` → panel not in the Network group, URL → redirect or 403).
- [ ] **Step 2:** `make -C frontend test` → FAIL (package missing).
- [ ] **Step 3:** Implement the skeleton, panel, enabled file, settings and the index view stub.
- [ ] **Step 4:** `make -C frontend test` → PASS.
- [ ] **Step 5: Commit** `feat(frontend): horizon plugin skeleton and test harness`

### Task 2: API client

**Files:** Create `frontend/router_peering_dashboard/api/{__init__,peering}.py`, `.../api/tests.py`

**Interfaces:**
- Produces, in `router_peering_dashboard.api.peering`:
  ```python
  class APIError(Exception):  # .status: int, .message: str, .overlaps: list[str]
  def list(request, direction=None) -> list[dict]          # GET /v1/peerings?direction=
  def get(request, peering_id) -> dict
  def events(request, peering_id) -> list[dict]
  def create(request, name, router_id, peer_project_id) -> dict
  def accept(request, peering_id, router_id) -> dict
  def reject(request, peering_id) -> dict
  def repair(request, peering_id) -> dict
  def delete(request, peering_id) -> dict | None
  ```
  Non-2xx → `APIError(status, body["error"], body.get("overlaps", []))`. A timeout or connection error → `APIError(503, "router peering service unreachable")`.

- [ ] **Step 1:** Tests with `requests.request` patched: right method, URL, `X-Auth-Token` and JSON body for each function; 409 with `overlaps` → `APIError.overlaps`; `requests.ConnectionError` → `APIError(503)`; the URL comes from `url_for(request, 'router-peering')`.
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(frontend): router-peering API client`

### Task 3: Index — Outgoing / Incoming tabs, tables, row actions

**Files:** Create `.../router_peering/{tables,tabs}.py`; modify `views.py`, `index.html`, `tests.py`

**Interfaces:**
- `PeeringsTable(tables.DataTable)`. Columns: name (link to detail), peer project ID, my router, status (with `status_reason` as a tooltip), next hop, expires (pending only). Table actions: `CreatePeering` (link, Outgoing tab only). Row actions: `AcceptPeering` (link to the modal), `RejectPeering`, `CancelPeering`, `RepairPeering`, `DeletePeering`. The batch and single actions call the API module. `allowed()` follows Review Focus 4, using the view's `role` field (`requester` / `accepter`) and `status`.
- `PeeringTabs(tabs.TabGroup)` with `OutgoingTab` (`direction=outgoing`) and `IncomingTab` (`direction=incoming`). `APIError` → `exceptions.handle(request, msg)` and an empty list.

- [ ] **Step 1:** Tests: `test_index_lists_both_tabs` (outgoing and incoming rows render in their own tab), `test_row_actions_by_status` (a table over status × role: which action names appear), `test_index_api_error` (`list` raises `APIError(503)` → 200, error message, empty tables), `test_delete_action_calls_api`, `test_reject_action_calls_api`.
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(frontend): peering list with incoming and outgoing tabs`

### Task 4: Create form

**Files:** Create `.../router_peering/forms.py`; modify `views.py`, `urls.py`, templates `_create.html` and `create.html`, `tests.py`

**Interfaces:**
- `CreatePeeringForm(forms.SelfHandlingForm)`. Fields: `name` (max 255), `router_id` (choices from `api.neutron.router_list(request, tenant_id=request.user.project_id)`, labelled `name (id[:8])`), `peer_project_id` (help text: "Ask the other project for its ID: `openstack token issue -c project_id`"). `handle` → `api.peering.create`; `APIError` → `exceptions.handle` with the API message; success → message "Peering requested. The other project must accept it." and a redirect to index.
- `CreateView(forms.ModalFormView)` at `create/`.

- [ ] **Step 1:** Tests: `test_create_get` (dropdown lists the project's routers), `test_create_post_ok` (the API is called with the exact values, then a redirect), `test_create_api_error` (409 limit → form error shows the API text), `test_create_no_routers` (Review Focus 5).
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(frontend): request a peering`

### Task 5: Accept form

**Files:** Modify `forms.py`, `views.py`, `urls.py`, templates `_accept.html` and `accept.html`, `tests.py`

**Interfaces:**
- `AcceptPeeringForm(SelfHandlingForm)`. Hidden `peering_id`, `router_id` choices as in Task 4, and a read-only display of the requester project name and ID taken from `api.peering.get`. `handle` → `api.peering.accept`. On `APIError` with `overlaps`, the error text is `"<message>: <cidr>, <cidr>"`. On success → "Peering accepted. Building the transit network…".
- `AcceptView` at `<peering_id>/accept/`.

- [ ] **Step 1:** Tests: `test_accept_get_shows_requester`, `test_accept_post_ok`, `test_accept_overlap_message` (Review Focus 2).
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(frontend): accept a peering`

### Task 6: Detail view

**Files:** Modify `views.py`, `urls.py`, templates `detail.html` and `_detail_overview.html`, `tables.py` (`EventsTable`), `tests.py`

**Interfaces:**
- `DetailView` at `<peering_id>/`, built from `api.peering.get` plus `api.peering.events`. It shows:
  - the overview: name, status and reason, role, my router, peer project ID (and name for the accepter), next hop;
  - peer CIDRs and warnings;
  - **route hints** as `<pre>` blocks, one command per line;
  - `EventsTable`: time, action, actor (blank when the API hides it).

  Breadcrumb back to the index. `APIError(404)` → redirect to the index with an error.

- [ ] **Step 1:** Tests: `test_detail_active_shows_hints` (the exact hint text from the API is in the page), `test_detail_pending_no_hints`, `test_detail_events`, `test_detail_not_found_redirects`.
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(frontend): peering detail with route hints and events`

### Task 7: Delivery: mount config, custom-image fallback, CI

**Files:**
- Create: `deploy/kolla/horizon/README.md`, `deploy/kolla/horizon/globals-snippet.yml`, `deploy/kolla/horizon/_9999-custom-settings.py.snippet`, `deploy/kolla/horizon/Dockerfile`
- Modify: `.github/workflows/ci.yml`, `README.md`

**Interfaces:**
- **Mount option.** `globals-snippet.yml`:
  ```yaml
  horizon_extra_volumes: "{{ default_extra_volumes + ['<dir>/router_peering_dashboard:/var/lib/kolla/venv/lib/python3/site-packages/router_peering_dashboard:ro', '<dir>/router_peering_dashboard/enabled/_1490_project_router_peering_panel.py:/var/lib/kolla/venv/lib/python3/site-packages/openstack_dashboard/local/enabled/_1490_project_router_peering_panel.py:ro'] }}"
  ```
  The custom-settings snippet carries `# router-peering-dashboard <version>`, which forces Kolla to re-run `collectstatic` (its hash covers `local_settings.d`).
- **Image option** (fallback). `Dockerfile` `FROM quay.io/openstack.kolla/horizon:${RELEASE}-ubuntu-noble`, then `pip install /src`, then copy the enabled file.
- **CI.** A new job `frontend-test` runs `make -C frontend test` on every push and PR.

- [ ] **Step 1:** Write the files. `actionlint` passes. `docker build deploy/kolla/horizon` (context `frontend/`) succeeds, and `python -c "import router_peering_dashboard"` works in the built image.
- [ ] **Step 2:** Commit `feat(deploy): horizon plugin delivery by mount or image; CI for frontend`
- [ ] **Step 3 (the test cloud, needs approval for each command):** copy `frontend/router_peering_dashboard` to `.04:/opt/router-peering/dashboard/`; back up `globals.yml`; add the snippet and the custom-settings line; run `kolla-ansible reconfigure -i multinode --tags horizon`. Check:
  - `/dashboard/project/router_peering/` gives 302 to login (not 404);
  - the Horizon logs are clean;
  - you log in and see the panel.

  If the mount fails, record why and switch to the image option.
