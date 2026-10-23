# Router Peering Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `router-peering` Go binary (API + reconciler + migrations) and its Kolla deploy files, so 2 OpenStack projects can request, accept and delete router peerings through a REST API.

**Architecture:** One Go binary with subcommands `api`, `reconciler`, `migrate`. The API validates Keystone tokens and only writes Postgres rows. The reconciler claims rows with a lease and does every Neutron write with the service account. Neutron and Keystone sit behind small interfaces with in-memory fakes, so the state machine is tested without a cloud; one `e2e` test runs against a real cloud.

**Tech Stack:** Go 1.23, `net/http` (Go 1.22 pattern routing, no web framework), `github.com/jackc/pgx/v5` (pgxpool), `github.com/gophercloud/gophercloud/v2`, `gopkg.in/yaml.v3`, `net/netip`, Postgres 16.

**Spec:** `docs/superpowers/specs/2026-10-23-router-peering-design.md` (read §6–§9 before any task).

**Out of scope (own plans later):** `client/` (OSC plugin), `dashboard/` and `deploy/kolla/horizon/` (Horizon plugin), DevStack CI.

## Global Constraints

- Module path `github.com/zufardhiyaulhaq/openstack-router-peering`. License Apache-2.0.
- Peering ID: `rpx-` + 12 lowercase hex. Every Neutron object the service creates is named `rpx-<id>` (ports `rpx-<id>-requester` / `rpx-<id>-accepter`) and tagged `rpx-<id>`.
- Statuses exactly: `pending_acceptance, provisioning, active, degraded, rejected, cancelled, expired, failed, deleting, deleted`. Terminal: `rejected, cancelled, expired, failed, deleted`.
- Event actions exactly: `requested, accepted, rejected, cancelled, expired, provision_step, provisioned, degraded, repair_requested, repaired, delete_requested, delete_blocked, deleted, project_gone, error`.
- Defaults: transit pool `100.64.0.0/16` in /29 blocks (first usable IP = requester, second = accepter); pending TTL 168h; limit 50 non-terminal peerings per project; work interval 10s; expiry 1m; drift 5m; orphan sweep 1h; pool scan 1h; orphan grace 1h; lease 2m; backoff 5s doubling, cap 5m; token cache 5m; listen `:9797`.
- Roles default: request/accept/reject/repair/delete = `member`, view = `reader`. A role check passes when the token's role list contains the configured role (Keystone puts implied roles in the token, so `admin` passes `member`).
- The API never calls a Neutron write method. Only `internal/reconciler` may.
- Nullable DB columns map to Go zero values (`""`, invalid `netip.Prefix`/`netip.Addr`, zero `time.Time`); `store` converts both ways.
- Secrets only from env: `RP_DATABASE_URL`, `RP_KEYSTONE_PASSWORD` override the config file.
- DB tests need `RP_TEST_DATABASE_URL`; they `t.Skip` without it. `make test-db` starts `postgres:16` in Docker and exports it.

## Review Focus

1. **Accept racing cancel / reject / a second accept** → exactly one wins; the others get `409`. (Task 4 test `TestTransitionConflict`, Task 12 test `TestAcceptRacesCancel`.)
2. **Delete while `provisioning`** → allowed (plan's call, not in spec §6.2): status becomes `deleting`; the provisioning worker's next write fails with `ErrLeaseLost`/conflict and it stops; the delete sequence cleans up partial objects. (Task 6 `TestStaleWorkerCannotWrite`, Task 9 `TestDeleteDuringProvisioning`.)
3. **Requester router deleted before accept** → accept returns `409` "requester router no longer exists"; nothing is built. (Task 12 `TestAcceptRequesterRouterGone`.)
4. **Transit pool exhausted** → peering stays `provisioning` with `status_reason` "transit pool exhausted" and retries with backoff (operator extends the pool); it is not `failed`. (Task 8 `TestProvisionPoolExhausted`.)
5. **Token scoped to a project on neither side, or unscoped** → `404` for a peering, `401` for an unscoped/invalid token; never leaks the peering. (Task 11 `TestForeignProjectGets404`, `TestUnscopedToken401`.)

---

### Task 1: Repo bootstrap and config

**Files:**
- Create: `go.mod`, `LICENSE` (Apache-2.0 text), `Makefile`, `.gitignore`, `cmd/router-peering/main.go`, `internal/config/config.go`, `internal/config/config_test.go`, `deploy/kolla/router-peering.yaml.example`

**Interfaces:**
- Produces:
  ```go
  type Config struct {
      Listen string; DatabaseURL string
      Keystone KeystoneConfig            // AuthURL, Username, Password, ProjectName, UserDomainName, ProjectDomainName, Region, Interface
      CloudAdminProject string           // default "admin"
      TransitPool netip.Prefix
      Roles Roles                        // Request, Accept, Reject, Repair, Delete, View string
      MaxNonTerminalPerProject int
      PendingTTL, Lease, OrphanGrace, TokenCacheTTL time.Duration
      Intervals Intervals                // Work, Expiry, Drift, Sweep, PoolScan time.Duration
      Backoff BackoffConfig              // Initial, Max time.Duration
  }
  func Defaults() Config
  func Load(path string) (Config, error)   // Defaults() overlaid by YAML file, then env overrides, then Validate
  func (c Config) Validate() error
  ```
- `main.go`: `router-peering <api|reconciler|migrate> --config <path>`; unknown subcommand exits 2 with usage. Bodies are stubs until Task 13.

- [ ] **Step 1: Write failing tests** in `internal/config/config_test.go`:
  - `TestDefaults`: `Defaults().TransitPool == netip.MustParsePrefix("100.64.0.0/16")`, `Listen == ":9797"`, `MaxNonTerminalPerProject == 50`, `PendingTTL == 168*time.Hour`, `Roles.Accept == "member"`, `Roles.View == "reader"`, `Backoff.Max == 5*time.Minute`.
  - `TestLoadOverlayAndEnv`: YAML sets `transit_pool: 10.250.0.0/20` and `database_url: x`; env `RP_DATABASE_URL=y`, `RP_KEYSTONE_PASSWORD=s` → `TransitPool` is the YAML value, `DatabaseURL == "y"`, `Keystone.Password == "s"`.
  - `TestValidateRejects`: pool `/30` or longer → error; IPv6 pool → error; empty `DatabaseURL` → error.
- [ ] **Step 2:** `go test ./internal/config/...` → FAIL (package missing).
- [ ] **Step 3:** Implement `config.go` (YAML keys snake_case of the field names; durations as Go duration strings). Write `router-peering.yaml.example` with every key and its default, password omitted.
- [ ] **Step 4:** `go test ./internal/config/...` → PASS. `go build ./...` → OK.
- [ ] **Step 5:** Makefile targets: `test` (`go test ./...`), `test-db` (start `postgres:16` container on 55432, export `RP_TEST_DATABASE_URL`, run `go test ./...`), `lint` (`go vet ./...`).
- [ ] **Step 6: Commit** `feat: repo bootstrap and config`

### Task 2: Model

**Files:**
- Create: `internal/model/model.go`, `internal/model/model_test.go`

**Interfaces:**
- Produces:
  ```go
  type Status string   // the 10 constants: StatusPendingAcceptance ... StatusDeleted
  func (s Status) Terminal() bool
  type Peering struct {
      ID, Name string; Status Status; StatusReason string
      RequesterProjectID, RequesterRouterID, AccepterProjectID, AccepterRouterID string
      TransitCIDR netip.Prefix
      TransitNetworkID, TransitSubnetID, RequesterPortID, AccepterPortID string
      RequesterIP, AccepterIP netip.Addr
      Attempts int; NextAttemptAt time.Time; ClaimedBy string; ClaimedUntil time.Time
      Warnings []string
      CreatedBy, AcceptedBy, DeletedBy string
      CreatedAt, UpdatedAt, ExpiresAt, DeletedAt time.Time
  }
  type Event struct { ID int64; PeeringID string; At time.Time; ActorUserID, ActorProjectID, Action string; Detail map[string]any }
  func NewID() string                                     // "rpx-" + 12 hex, crypto/rand
  func ValidProjectID(s string) bool                      // non-empty, <= 64 chars, no whitespace
  func BlockIPs(p netip.Prefix) (requester, accepter netip.Addr)   // network+1, network+2
  func Overlaps(a, b []netip.Prefix) []netip.Prefix      // members of a overlapping any of b, deduped, sorted
  func Tag(id string) string                              // returns id (the tag equals the ID); one place to change it
  ```

- [ ] **Step 1: Write failing tests:**
  - `TestNewIDFormat`: matches `^rpx-[0-9a-f]{12}$`; 1000 calls give 1000 distinct values.
  - `TestTerminal`: exactly the 5 terminal statuses return true.
  - `TestBlockIPs`: `100.64.0.8/29` → `100.64.0.9`, `100.64.0.10`.
  - `TestOverlaps`: `[10.0.0.0/24, 198.51.100.0/24]` vs `[10.0.0.128/25]` → `[10.0.0.0/24]`; no overlap → empty.
  - `TestValidProjectID`: `"8f3c1a9e2b7d4e6f9a0b1c2d3e4f5a6b"` true; `""`, 65 chars, `"a b"` false.
- [ ] **Step 2:** `go test ./internal/model/...` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5: Commit** `feat: peering model`

### Task 3: Migrations and store base

**Files:**
- Create: `migrations/0001_init.sql`, `migrations/embed.go`, `internal/store/store.go`, `internal/store/store_test.go`, `internal/store/testdb_test.go`

**Interfaces:**
- Produces:
  ```go
  var ErrNotFound, ErrConflict, ErrLeaseLost, ErrPoolExhausted error
  type Store struct { /* *pgxpool.Pool */ }
  func Open(ctx context.Context, url string) (*Store, error)
  func (s *Store) Close()
  func (s *Store) Migrate(ctx context.Context) error   // applies embedded migrations/*.sql in order, records them in schema_migrations, takes pg_advisory_lock so 2 starters don't race
  func (s *Store) Ping(ctx context.Context) error
  ```
  `testdb_test.go`: `func newTestStore(t *testing.T) *Store` — skips without `RP_TEST_DATABASE_URL`, creates a fresh schema per test (`CREATE SCHEMA t_<rand>`, `search_path`), migrates, drops on cleanup. Later store tasks reuse it.

- [ ] **Step 1:** `0001_init.sql` = spec §6.1 verbatim (enum, 3 tables, indexes).
- [ ] **Step 2: Write failing tests:**
  - `TestMigrateIdempotent`: `Migrate` twice → no error; `schema_migrations` has 1 row.
  - `TestSameProjectRejected`: raw insert with equal project IDs → check violation.
  - `TestRouterPairIndex`: 2 `pending_acceptance` rows from router R to different projects with NULL accepter router → both insert. Set both accepter routers to the same S (status `provisioning`) → second update fails with unique violation. Mark one `deleted` → the other succeeds.
- [ ] **Step 3:** `make test-db` → FAIL.
- [ ] **Step 4:** Implement `store.go`.
- [ ] **Step 5:** `make test-db` → PASS.
- [ ] **Step 6: Commit** `feat: schema and migrations`

### Task 4: Store — peerings and events

**Files:**
- Create: `internal/store/peerings.go`, `internal/store/peerings_test.go`

**Interfaces:**
- Consumes: Task 2 model, Task 3 `Store`, errors.
- Produces:
  ```go
  func (s *Store) CreatePeering(ctx context.Context, p *model.Peering) error   // sets p.ID via model.NewID, retries on PK clash; writes event "requested" in the same tx
  func (s *Store) GetPeering(ctx context.Context, id string) (model.Peering, error)  // ErrNotFound
  type ListFilter struct { ProjectID string; Direction string /* "incoming"|"outgoing"|"" = both */; Statuses []model.Status; All bool /* cloud admin: ignore ProjectID */; Limit int; Marker string }
  func (s *Store) ListPeerings(ctx context.Context, f ListFilter) ([]model.Peering, error)  // ordered by created_at, id; Limit default 100, max 1000
  func (s *Store) ListByStatus(ctx context.Context, st ...model.Status) ([]model.Peering, error)
  func (s *Store) CountNonTerminal(ctx context.Context, projectID string) (int, error)  // as requester or accepter
  type Patch struct { StatusReason, AccepterRouterID, AcceptedBy, DeletedBy *string; Warnings *[]string; ResetAttempts bool }
  func (s *Store) Transition(ctx context.Context, id string, from []model.Status, to model.Status, patch Patch, ev model.Event) (model.Peering, error)
      // single UPDATE ... WHERE id=$1 AND status = ANY($from); 0 rows → ErrConflict (or ErrNotFound if id unknown);
      // unique violation on router pair → ErrConflict; to=deleting also clears claimed_by/claimed_until and sets next_attempt_at=now();
      // to=deleted sets deleted_at; writes ev (Action required) in the same tx; bumps updated_at
  func (s *Store) AddEvent(ctx context.Context, e model.Event) error
  func (s *Store) ListEvents(ctx context.Context, peeringID string) ([]model.Event, error)
  func (s *Store) LastAction(ctx context.Context, peeringID string, skip ...string) (string, error)  // newest event action not in skip; "" if none
  ```

- [ ] **Step 1: Write failing tests:** `TestCreateGet` (round-trips every field incl. empty → NULL), `TestListDirection` (A sees its request as outgoing, B as incoming, C sees nothing), `TestTransitionConflict` (2 goroutines `Transition(pending→provisioning)` and `Transition(pending→cancelled)`: exactly one succeeds, the other gets `ErrConflict`), `TestTransitionWritesEvent`, `TestCountNonTerminal` (terminal rows not counted).
- [ ] **Step 2:** `make test-db` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5: Commit** `feat: peering store`

### Task 5: Store — transit pool

**Files:**
- Create: `internal/store/pool.go`, `internal/store/pool_test.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Store) SeedBlocks(ctx context.Context, pool netip.Prefix) (added int, err error)  // INSERT ... ON CONFLICT DO NOTHING, all /29s
  func (s *Store) AllocateBlock(ctx context.Context, peeringID string) (netip.Prefix, error)
      // returns the block already held by peeringID if any (idempotent); else spec §6.3 SQL; none free → ErrPoolExhausted
  func (s *Store) FreeBlock(ctx context.Context, peeringID string) error
  func (s *Store) SetReserved(ctx context.Context, used []netip.Prefix) (reserved int, err error)
      // free blocks overlapping any of used → reserved=true; free blocks overlapping none → reserved=false; held blocks untouched
  ```

- [ ] **Step 1: Write failing tests:**
  - `TestSeedIdempotent`: seed `100.64.0.0/24` → 32; again → 0.
  - `TestAllocateConcurrent` (spec §12 gate): seed `/24` (32 blocks), 32 peerings, 32 goroutines allocate → 32 distinct blocks; a 33rd → `ErrPoolExhausted`.
  - `TestAllocateIdempotent`: same peering twice → same block.
  - `TestReservedSkipped`: `SetReserved([100.64.0.0/28])` → blocks `.0/29` and `.8/29` never allocated; `SetReserved(nil)` → they come back.
  - `TestFreeBlock`: freed block is allocatable again.
- [ ] **Step 2–4:** FAIL → implement → PASS (`make test-db`).
- [ ] **Step 5: Commit** `feat: transit block pool`

### Task 6: Store — leases and worker writes

**Files:**
- Create: `internal/store/lease.go`, `internal/store/lease_test.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Store) ClaimWork(ctx context.Context, me string, limit int, lease time.Duration) ([]model.Peering, error)  // spec §7.1 SQL, returns full rows
  func (s *Store) ExtendLease(ctx context.Context, id, me string, lease time.Duration) error   // ErrLeaseLost
  type WorkerPatch struct { // pointer = set
      StatusReason *string; TransitCIDR *netip.Prefix; RequesterIP, AccepterIP *netip.Addr
      TransitNetworkID, TransitSubnetID, RequesterPortID, AccepterPortID *string
  }
  func (s *Store) SaveStep(ctx context.Context, id, me string, expect model.Status, patch WorkerPatch, ev *model.Event) error
      // UPDATE ... WHERE id AND claimed_by=me AND status=expect; 0 rows → ErrLeaseLost
  func (s *Store) Finish(ctx context.Context, id, me string, expect, to model.Status, patch WorkerPatch, ev model.Event) error
      // sets status=to, clears claim, attempts=0; 0 rows → ErrLeaseLost
  func (s *Store) Retry(ctx context.Context, id, me string, expect model.Status, reason string, next time.Time) error
      // attempts+1, next_attempt_at=next, status_reason=reason, clears claim; 0 rows → ErrLeaseLost
  ```

- [ ] **Step 1: Write failing tests:**
  - `TestClaimNoOverlap`: 20 `provisioning` rows, 2 claimers with limit 10 concurrently → 20 distinct IDs total.
  - `TestExpiredLeaseReclaimable`: claim with 1ms lease, sleep 5ms, second claimer gets it.
  - `TestStaleWorkerCannotWrite`: worker W1 claims; API runs `Transition(provisioning→deleting)`; W1 `SaveStep(expect provisioning)` → `ErrLeaseLost`.
  - `TestNotDueNotClaimed`: `next_attempt_at` in the future → not claimed.
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat: reconciler leases`

### Task 7: Neutron and Keystone clients (interfaces, fakes, real)

**Files:**
- Create: `internal/neutron/neutron.go` (interface, types, errors), `internal/neutron/gophercloud.go`, `internal/neutron/gophercloud_test.go`, `internal/neutron/fake/fake.go`, `internal/neutron/fake/fake_test.go`
- Create: `internal/keystone/keystone.go`, `internal/keystone/gophercloud.go`, `internal/keystone/cache.go`, `internal/keystone/keystone_test.go`, `internal/keystone/fake/fake.go`

**Interfaces:**
- Produces (neutron):
  ```go
  var ErrNotFound, ErrInUseByRoute error           // wrap with fmt.Errorf("%w: ...")
  type Kind int  // KindTransient, KindHard, KindNotFound, KindInUseByRoute
  func KindOf(err error) Kind   // 5xx/timeout/conn/409-not-route → Transient; 404 → NotFound; 409 RouterInterfaceInUseByRoute → InUseByRoute; other 4xx → Hard
  type Router struct { ID, ProjectID string; SubnetCIDRs []netip.Prefix }   // IPv4 subnets on router interfaces, excluding subnets named rpx-*
  type Port struct { ID, ProjectID, NetworkID, DeviceID, DeviceOwner string; IP netip.Addr }
  type Network struct { ID, Name string; Tags []string; CreatedAt time.Time }
  type Client interface {
      GetRouter(ctx, id string) (Router, error)
      CreateNetwork(ctx, name, tag string) (string, error)                      // port_security_enabled=false, not shared
      CreateSubnet(ctx, networkID, name, tag string, cidr netip.Prefix) (string, error)  // ipv4, gateway null, dhcp off
      CreatePort(ctx, networkID, subnetID, projectID, name, tag string, ip netip.Addr) (string, error)
      GetPort(ctx, id string) (Port, error)
      FindPortByIP(ctx, networkID string, ip netip.Addr) (Port, error)          // ErrNotFound; used to resume after a crash before the ID was saved
      FindNetworkByName(ctx, name string) (Network, error)
      FindSubnetByName(ctx, name string) (string, error)
      AddRouterInterface(ctx, routerID, portID string) error
      RemoveRouterInterface(ctx, routerID, portID string) error
      DeletePort(ctx, id string) error; DeleteSubnet(ctx, id string) error; DeleteNetwork(ctx, id string) error
      ListPortsByTag(ctx, tag string) ([]Port, error)
      ListOwnNetworks(ctx) ([]Network, error)                                   // service project only
      ListAllSubnetCIDRs(ctx) ([]netip.Prefix, error)                           // all projects, IPv4
  }
  ```
  Real client: if the `tag-creation` extension is absent (checked once at start), create then `PUT .../tags/<tag>`.
- Produces (keystone):
  ```go
  var ErrUnauthorized, ErrNotFound error
  type Token struct { UserID, ProjectID, ProjectName, DomainID string; Roles []string; ExpiresAt time.Time }
  type Project struct { ID, Name, DomainID string; Enabled bool }
  type Client interface { Validate(ctx, token string) (Token, error); GetProject(ctx, id string) (Project, error) }
  func NewCached(c Client, ttl time.Duration) Client   // caches Validate by sha256(token), min(ttl, ExpiresAt)
  ```
- Fakes: `neutron/fake.New()` stores routers, networks, subnets, ports in maps; `AddRouterInterface` sets DeviceOwner `network:router_interface` and adds the subnet CIDR to the router; `RemoveRouterInterface` deletes the port, but returns `ErrInUseByRoute` instead if `fake.SetRoute(routerID, nexthop)` names that port's IP; `AddRouterInterface` returns a Hard 400 error if the port's subnet overlaps a CIDR already on the router. `fake.FailNext(method string, err error)` injects one failure. `keystone/fake` maps tokens and projects.

- [ ] **Step 1: Write failing tests:**
  - `TestKindOf` table: 503 → Transient, 404 → NotFound, 409 body `{"NeutronError":{"type":"RouterInterfaceInUseByRoute"}}` → InUseByRoute, 409 other → Transient, 400 → Hard, `context.DeadlineExceeded` → Transient.
  - `TestTagFallback` (httptest Neutron): extensions list without `tag-creation` → `CreateNetwork` issues POST then `PUT /v2.0/networks/{id}/tags/rpx-x`.
  - `TestGetRouterExcludesTransit` (httptest): router with subnets `10.0.0.0/24` and `rpx-…` `100.64.0.8/29` → `SubnetCIDRs == [10.0.0.0/24]`.
  - `TestFakeRemoveBlockedByRoute`, `TestFakeOverlapOnAttach`.
  - `TestCacheValidate`: second `Validate` within TTL doesn't hit the inner client; after `ExpiresAt` it does.
  - `TestKeystoneValidate` (httptest): `GET /v3/auth/tokens` with `X-Subject-Token` → parses user, project, roles; 404 → `ErrUnauthorized`.
- [ ] **Step 2–4:** FAIL → implement → PASS (`make test`).
- [ ] **Step 5: Commit** `feat: neutron and keystone clients`

### Task 8: Reconciler — provision and repair

**Files:**
- Create: `internal/reconciler/reconciler.go`, `internal/reconciler/provision.go`, `internal/reconciler/provision_test.go`, `internal/reconciler/helpers_test.go`

**Interfaces:**
- Consumes: Tasks 4–7.
- Produces:
  ```go
  type Reconciler struct { Store *store.Store; Neutron neutron.Client; Keystone keystone.Client; Cfg config.Config; Me string; Now func() time.Time }
  func Backoff(attempts int, initial, max time.Duration) time.Duration   // initial * 2^(attempts), capped
  func (r *Reconciler) Process(ctx context.Context, p model.Peering) error  // dispatch on p.Status: provisioning → provision, deleting → teardown
  func (r *Reconciler) provision(ctx context.Context, p model.Peering) error
  ```
  Provision follows spec §7.2 steps 0–8 in order. Before each create it checks the stored ID, then a lookup (`FindNetworkByName`, `FindSubnetByName`, `FindPortByIP`), and only then creates. After each step: `SaveStep` (with a `provision_step` event naming the step) then `ExtendLease`. Step 8 → `Finish(provisioning→active)` with event `provisioned`, or `repaired` when `LastAction(id, "provision_step", "error")` is `repair_requested`.
  Errors: `ErrLeaseLost` → return nil (someone else owns it). `ErrPoolExhausted` → `Retry` with reason `transit pool exhausted`. `neutron.KindTransient` → `Retry` with `Backoff`. `KindHard` / router gone / owner changed → run `r.teardown` on what exists, `FreeBlock`, `Finish(→failed)` with Neutron's message, event `error`.

- [ ] **Step 1: Write failing tests** (real store via `newTestStore`-like helper, fake Neutron/Keystone):
  - `TestProvisionHappyPath`: ends `active`; fake has 1 network `rpx-<id>`, 1 subnet with gateway none, 2 ports at `.9`/`.10` owned by A/B and attached to the right routers; row has all IDs.
  - `TestProvisionResumesAfterCrash`: for each step 2–7, `FailNext` with a transient error, run `Process` until `active` → still exactly 1 network, 1 subnet, 2 ports.
  - `TestProvisionCrashBeforeIDSaved`: create the port in the fake directly with the expected IP, no ID in DB → provision adopts it (no second port).
  - `TestProvisionHardErrorRollsBack`: fake overlap on attaching B → status `failed`, `status_reason` contains Neutron message, fake has no `rpx-<id>` objects, block free.
  - `TestProvisionPoolExhausted` (Review Focus 4).
  - `TestRepairRecreatesMissingSide`: active peering, delete A's port in the fake, set status `provisioning` → only a new A port at the same IP; B port ID unchanged; event `repaired`.
  - `TestBackoff`: attempts 0→5s, 1→10s, 10→5m.
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat: provision and repair`

### Task 9: Reconciler — delete

**Files:**
- Create: `internal/reconciler/delete.go`, `internal/reconciler/delete_test.go`

**Interfaces:**
- Produces: `func (r *Reconciler) teardown(ctx context.Context, p model.Peering) error` — spec §7.3 steps 1–6. `KindNotFound` on any step = already done, continue. `KindInUseByRoute` → `Retry` with reason `remove routes via <ip> on router <id> first` and event `delete_blocked` (once per block, not every retry: only if the previous `status_reason` differs). Block freed only after `DeleteNetwork` succeeds or is NotFound. Done → `Finish(deleting→deleted)` + event `deleted`.

- [ ] **Step 1: Write failing tests:**
  - `TestDeleteCleansEverything`: active peering → `deleted`, fake empty, block free, `deleted_at` set.
  - `TestDeleteBlockedByRoute`: `fake.SetRoute(A, accepterIP)` → stays `deleting`, reason names the IP, one `delete_blocked` event after 3 runs; clear route → `deleted`.
  - `TestDeleteToleratesMissing`: ports already gone → `deleted`.
  - `TestDeleteDuringProvisioning` (Review Focus 2): provision with `FailNext` after step 4, API-style `Transition(provisioning→deleting)`, run `Process` → `deleted`, fake empty.
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat: delete sequence`

### Task 10: Reconciler — periodic jobs and run loop

**Files:**
- Create: `internal/reconciler/jobs.go`, `internal/reconciler/jobs_test.go`, `internal/reconciler/run.go`

**Interfaces:**
- Produces:
  ```go
  func (r *Reconciler) WorkOnce(ctx context.Context) error      // ClaimWork(limit 10) → Process each
  func (r *Reconciler) ExpireOnce(ctx context.Context) error    // pending past expires_at → expired (event expired)
  func (r *Reconciler) DriftOnce(ctx context.Context) error
  func (r *Reconciler) SweepOnce(ctx context.Context) error
  func (r *Reconciler) PoolScanOnce(ctx context.Context) error  // SeedBlocks(Cfg.TransitPool) then SetReserved(ListAllSubnetCIDRs minus subnets named rpx-*)
  func (r *Reconciler) Run(ctx context.Context) error           // PoolScanOnce at start, then one ticker per interval; a job error is logged, never fatal
  ```
  Drift, per `active`/`degraded` row:
  1. `GetProject` on both: `ErrNotFound` → `pending_acceptance` rows → `cancelled`; others → `deleting`; event `project_gone`. Disabled → no change.
  2. Both ports exist and `DeviceID` matches the recorded router, else `active → degraded` with reason `interface on router <id> is missing` (event `degraded`).
  3. Overlap: `model.Overlaps(A.SubnetCIDRs, B.SubnetCIDRs)` → write `Warnings` as `"<cidr> overlaps with the peer"` strings (empty list clears).
  Drift also runs step 1 for `pending_acceptance` rows. Sweep: `ListOwnNetworks`, names with prefix `rpx-`, no non-terminal row with that ID, `CreatedAt` older than `OrphanGrace` → delete ports by tag, subnet, network.

- [ ] **Step 1: Write failing tests:** `TestExpire` (with `Now` set 8 days ahead), `TestDriftDegraded`, `TestDriftProjectGoneActive` (→ deleting), `TestDriftProjectGonePending` (→ cancelled), `TestDriftProjectDisabledNoChange`, `TestDriftWarnings` (add overlapping CIDR to router B in the fake → warning; remove → cleared), `TestSweepOrphan` (orphan older than grace deleted, younger kept, live peering's network kept), `TestPoolScanReserves`.
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat: reconciler jobs`

### Task 11: API — auth, view, create, list, get, events

**Files:**
- Create: `internal/api/api.go`, `internal/api/auth.go`, `internal/api/view.go`, `internal/api/read.go`, `internal/api/create.go`, `internal/api/api_test.go`, `internal/api/view_test.go`

**Interfaces:**
- Consumes: store, keystone, neutron (read methods only: `GetRouter`), config, model.
- Produces:
  ```go
  func New(st *store.Store, ks keystone.Client, nc neutron.Client, cfg config.Config) http.Handler
  type View struct {
      ID, Name, Status, StatusReason string
      Role string               // "requester" | "accepter"
      RouterID string
      PeerProjectID string
      PeerProjectName string   `json:",omitempty"` // accepter view only
      NextHop string           `json:",omitempty"`
      PeerCIDRs, RouteHints, Warnings []string
      CreatedAt time.Time; ExpiresAt time.Time
  }
  func BuildView(p model.Peering, callerProject, peerName string, peerCIDRs []netip.Prefix) View
  ```
  JSON field names are snake_case (`peer_project_id`, `next_hop`, `route_hints` …). Route hint exact format: `openstack router set --route destination=<cidr>,gateway=<next_hop> <router_id>`, one per peer CIDR, only when `NextHop` is set. Errors are `{"error": "<message>"}`.
  Auth middleware: `X-Auth-Token` → `Validate`; invalid/unscoped (empty `ProjectID`) → 401. Cloud admin = token `ProjectName == Cfg.CloudAdminProject` and roles contain `admin`. A peering where the caller project is neither side (and not cloud admin) → 404.
  `POST /v1/peerings {name, router_id, peer_project_id}` → checks in order: role `request` (403), `ValidProjectID` (400), peer == own project (400), `GetProject(peer)` not found (404), router not found or not owned (400), `CountNonTerminal >= limit` (409) → 201 with requester view.
  `GET /v1/peerings?direction=&status=&limit=&marker=`, `GET /v1/peerings/{id}`, `GET /v1/peerings/{id}/events` need role `view`. `peer_cidrs` comes from `GetRouter(peer router)` only when the peer router is set; a Neutron error there leaves `peer_cidrs` empty, never fails the GET.

- [ ] **Step 1: Write failing tests:**
  - `TestBuildViewRequester` / `TestBuildViewAccepter`: requester sees `peer_project_id`=B, no name, `next_hop`=`accepter_ip`, hints with `gateway=100.64.0.10`; accepter sees A's name and `next_hop`=`requester_ip`.
  - `TestCreate201`, `TestCreateSameProject400`, `TestCreateUnknownPeer404`, `TestCreateRouterNotOwned400`, `TestCreateLimit409` (50 existing), `TestCreateNeedsMemberRole403` (reader token).
  - `TestForeignProjectGets404`, `TestUnscopedToken401` (Review Focus 5).
  - `TestListIncomingOutgoing`, `TestCloudAdminListsAll`.
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat: api read and create`

### Task 12: API — accept, reject, repair, delete

**Files:**
- Create: `internal/api/actions.go`, `internal/api/actions_test.go`

**Interfaces:**
- Consumes: Task 11 helpers; `store.Transition`; `model.Overlaps`.
- Produces handlers:
  - `POST /v1/peerings/{id}/accept {router_id}` (role `accept`, caller must be accepter): status must be `pending_acceptance` (409); `GetProject(requester)` missing or disabled → 409; `GetRouter(router_id)` missing or not owned → 400; `GetRouter(requester router)` missing → 409 `requester router no longer exists`; overlap per spec §9 table (A's CIDRs vs B's, A's vs CIDRs of every router in B's non-terminal peerings, and the mirror; transit subnets already excluded by `GetRouter`) → 409 with `{"error": "...", "overlaps": [...]}`; then `Transition(pending→provisioning, AccepterRouterID, AcceptedBy, event accepted)`; `ErrConflict` → 409. Response 202 with accepter view.
  - `POST .../reject` (role `reject`, accepter, pending) → 200 `rejected`.
  - `POST .../repair` (role `repair`, either side or cloud admin, only `degraded`) → `Transition(degraded→provisioning, ResetAttempts, event repair_requested)` → 202.
  - `DELETE /v1/peerings/{id}` (role `delete`, either side or cloud admin): `pending_acceptance` + requester → `cancelled` 200; `pending_acceptance` + accepter → 409 (use reject); `provisioning|active|degraded` → `deleting` 202 (event `delete_requested`, `DeletedBy`); `deleting` → 202 unchanged; terminal → 409.

- [ ] **Step 1: Write failing tests:**
  - `TestAccept202`, `TestAcceptByRequester404or403` (requester token → 403), `TestAcceptOverlap409` (both sides `10.0.0.0/24` → 409 lists it; status still pending), `TestAcceptOverlapWithPeersOfPeer409` (A already peered with C on `10.2.0.0/24`; B has `10.2.0.0/24` → 409), `TestAcceptRequesterRouterGone` (Review Focus 3), `TestAcceptRequesterProjectDisabled409`, `TestAcceptDuplicatePair409` (second peering between same routers).
  - `TestAcceptRacesCancel` (Review Focus 1): concurrent accept and requester DELETE → one 2xx, one 409.
  - `TestReject`, `TestRepairOnlyDegraded`, `TestDeleteMatrix` (table over status × side).
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat: api actions`

### Task 13: Binary wiring, health, image, Kolla deploy files

**Files:**
- Modify: `cmd/router-peering/main.go`
- Create: `internal/api/health.go`, `Dockerfile`, `deploy/kolla/docker-compose.yml`, `deploy/kolla/haproxy/router-peering.cfg`, `deploy/kolla/register.sh`, `README.md`

**Interfaces:**
- `migrate`: `Open` → `Migrate` → exit 0.
- `api`: `Open` → `Migrate` → `http.Server{Addr: Cfg.Listen, Handler: api.New(...)}` with `NewCached(keystone, TokenCacheTTL)`; graceful shutdown on SIGTERM.
- `reconciler`: `Open` → `Migrate` → `Reconciler{Me: hostname + "-" + pid}.Run`.
- `GET /healthz` → 200 always; `GET /readyz` → 200 if `Store.Ping` and a Keystone `GetProject` on the service project both succeed, else 503.
- Dockerfile: multi-stage `golang:1.23` → `gcr.io/distroless/static`, `ENTRYPOINT ["/router-peering"]`.
- `docker-compose.yml`: services `postgres` (`postgres:16`, volume `/var/lib/router-peering/pg`), `api` and `reconciler` (same image, `command: [api, --config, /etc/router-peering/router-peering.yaml]`), `restart: always`, `network_mode: host`, env `RP_DATABASE_URL`, `RP_KEYSTONE_PASSWORD` from an `.env` file not in the repo.
- `haproxy/router-peering.cfg`: a `listen router_peering` block binding `{{ kolla_internal_vip_address }}:9797` with `server` lines per controller `:9797`, `option httpchk GET /healthz`. Comment at top: copy to `/etc/kolla/config/haproxy/services.d/`, then `kolla-ansible reconfigure --tags haproxy`.
- `register.sh`: idempotent (`openstack ... show` before `create`): user `router-peering` (domain Default), role `admin` on project `service`, service `router-peering` type `router-peering`, endpoints public/internal/admin from env `RP_PUBLIC_URL`, `RP_INTERNAL_URL`, region `RP_REGION` (default `RegionOne`).
- README: what it is, the flow table from spec §4.1, deploy steps in order, the 2 open checks from spec §14.

- [ ] **Step 1:** Write failing test `TestReadyz` (`internal/api`): Ping OK + Keystone OK → 200; Keystone fake failing → 503.
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5:** Verify the default port: check Kolla-Ansible 2026.1's port list (`doc/source/reference/networking/...` or `ansible/roles/*/defaults/main.yml` grep for `9797`) shows no use of 9797. If used, pick another and update Global Constraints, config default and HAProxy file.
- [ ] **Step 6:** `docker build -t router-peering:dev .` → OK. `docker compose -f deploy/kolla/docker-compose.yml up postgres -d`, then `docker run --network host -e RP_DATABASE_URL=... router-peering:dev migrate --config deploy/kolla/router-peering.yaml.example` → exit 0. `bash -n deploy/kolla/register.sh` → OK.
- [ ] **Step 7: Commit** `feat: binary wiring and kolla deploy files`

### Task 14: End-to-end test against a real cloud

**Files:**
- Create: `test/e2e/e2e_test.go` (build tag `e2e`), `test/e2e/README.md`

**Interfaces:**
- Env: `RP_E2E_API_URL`, and 2 clouds.yaml entries `RP_E2E_CLOUD_A`, `RP_E2E_CLOUD_B` (member of 2 different projects), `RP_E2E_IMAGE`, `RP_E2E_FLAVOR`. The test creates its own routers, networks (`10.91.0.0/24` on A, `10.92.0.0/24` on B), security groups and 2 VMs, and deletes them at the end.

- [ ] **Step 1:** Write `TestE2E` with ordered subtests from spec §12: create → accept → active (poll 2 min) → add routes + SG rules → ping B's VM from A's VM via console-log or SSH through a FIP → delete blocked by route (status `deleting`, reason set) → remove routes → `deleted` → cancel → reject → degraded after manual `remove_router_interface` → repair → active → orphan sweep (create an `rpx-e2e…` network directly, run with grace 0 via `RP_E2E_ORPHAN` toggle or skip if not configurable). Expire is covered by unit tests only.
- [ ] **Step 2:** `go vet -tags e2e ./test/e2e/...` → OK.
- [ ] **Step 3:** Run on the test cloud after deploy (needs VPN; first resolve the 2 checks in spec §14): `go test -tags e2e ./test/e2e/... -v` → PASS. Record the run in `test/e2e/README.md`.
- [ ] **Step 4: Commit** `test: end-to-end suite`
