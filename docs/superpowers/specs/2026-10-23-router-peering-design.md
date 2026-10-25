# OpenStack Router Peering: design

Date: 2026-10-23 · Status: **approved** (2 facts open, §14)

Items marked **(call)** are tuning values left to the builder.

> **Updated 25/10 by `2026-10-25-peer-router-at-request-design.md`:** the requester names the peer **router** at request time and the accepter only confirms; `peer_cidrs` is no longer exposed; Horizon lists only live peerings, with a History tab. Where this document disagrees, the newer spec wins.

## 1. Intent

OpenStack has no VPC peering. Neutron has the parts (routers, networks, ports), but a tenant cannot
connect its router to another project's router without an admin. OpenStack also has no VPC object: the
closest thing is a router plus the subnets on it, and a project can own several routers (up to its
Neutron `router` quota). So the unit here is the **router**, and the feature is called **router peering**. Public clouds (AWS, GCP, Alibaba Cloud)
solve this with a request / accept flow. This project brings that flow to OpenStack as an
**open source service**: a small backend, a Horizon panel and an OpenStack CLI plugin.

The service does only the part a tenant cannot do alone: it creates a private transit network and
attaches both routers to it. Routes and security groups stay with the users, the same as on AWS.

First deployment target: the test cloud (Kolla-Ansible 2026.1, Neutron ML2/OVN, 5 hosts). The
project must not depend on anything cloud-specific.

## 2. Goals, non-goals, anti-goals

**Goals**
- A project can request peering between one of its routers and a router in another project, knowing
  only the other project's **Keystone project ID**. The 2 projects may be in different Keystone
  domains. Same-project peering is rejected.
- The other project accepts (choosing its own router) or rejects. Pending requests expire after 7 days.
- On accept, the service connects the 2 routers through a dedicated transit network and shows each side
  the **next hop** IP it needs for its routes.
- Either side can delete the peering. Delete removes everything the service created.
- Works from Horizon, from the `openstack` CLI, and from a REST API.

**Non-goals (v1)**
- Creating routes or security group rules for the user.
- Transitive routing (A↔B↔C). Not blocked by the service, but documented as unsupported.
- NAT between overlapping CIDRs.
- Cross-region or cross-cloud peering.
- DNS resolution across peers.
- IPv6 transit (the transit network is IPv4 only; tenant traffic is whatever the user routes).
- High availability for the database (§10). The database can change later.
- Notifications (email, chat, webhook). The accepter sees requests in Horizon's Incoming tab or hears
  about them directly from the requester.
- A cloud-admin shortcut that creates an already-accepted peering. Admins go through request / accept
  like everyone else (they can still list and delete any peering).
- Skyline (the newer OpenStack dashboard). Horizon only.
- A Terraform provider (later, built on the same API).

**Anti-goals (a failure even if it works)**
- A tenant can boot a VM or create a port on a transit network.
- The service changes any Neutron resource it did not create (everything it creates carries tag `rpx-<id>`).
- A backend or database outage breaks traffic on an existing peering.
- A user of project A learns anything about project B before B accepts, beyond "this ID exists".

## 3. Constraints

- **Runtime**: plain containers on the Kolla hosts (`docker run` / compose). No Kubernetes. Kolla does
  not manage these containers, so Kolla `upgrade` / `deploy` must not touch them.
- **Own database**: its own Postgres container. Not a schema in Kolla's MariaDB, so OpenStack upgrades
  stay independent.
- **Neutron**: needs the `router`, `extraroute`, `standard-attr-tag` and `port-security` extensions. Tested on ML2/OVN.
  ML2/OVS should work (same Neutron API) but is not a test target in v1.
- **Keystone v3** for identity, token validation and the service catalog.
- **Exposure**: a plain HTTP server on a configurable port, registered in the Keystone catalog as type
  `router-peering`. It runs standalone (catalog points at the host) or behind any load balancer; the Kolla
  HAProxy VIP is the documented HA option, not a requirement.

## 4. User experience

### 4.1 Flow

| # | Who | Action | Status after |
|---|---|---|---|
| 1 | Requester (project A) | Picks its router, enters B's project ID, **B's router ID** and a name | `pending_acceptance` |
| 2a | Accepter (project B) | **Confirms** the router the requester named, accepts. Refused with 409 on CIDR overlap (§9); the request stays pending | `provisioning` → `active` |
| 2b | Accepter | Rejects | `rejected` |
| 2c | Requester | Cancels before B answers | `cancelled` |
| 2d | Nobody, 7 days | — | `expired` |
| 3 | Both users | Add their own routes and security group rules | stays `active` |
| 4 | Either side | Deletes | `deleting` → `deleted` |

B gets the requester's project ID out of band. A finds its own ID with `openstack token issue -c project_id`
or Horizon → API Access. Non-admin users cannot list other projects, so the ID is the only handle. The API checks the format
loosely (non-empty, at most 64 characters) and relies on the Keystone lookup, because 32-hex IDs are
Keystone's default, not a guarantee.

### 4.2 What each side sees

| Field | Requester (A) | Accepter (B) |
|---|---|---|
| Peer identity | `peer_project_id` only | Requester project **name + ID** (looked up live in Keystone) |
| Own router | A's router | B's router (empty until accepted) |
| `next_hop` | B's transit IP | A's transit IP |
| `peer_router_id` | B's router | A's router |
| `route_hint` | `openstack router set --route destination=<peer subnet>,gateway=<next_hop> <router>` (one line, placeholder) | the mirror |
| `warnings` | overlap that appeared after accept (§9) | same |

A request to a project ID that does not exist returns 404. That reveals existence only, and default
Keystone IDs are random 32-hex values.

### 4.3 Horizon panel

**Project → Network → Router Peering.**
- List with 2 tabs: **Outgoing** and **Incoming**, with status and expiry.
- *Create* form: router dropdown (Neutron, user token), peer project ID, name.
- *Accept* modal on incoming rows: router dropdown, Accept / Reject.
- Detail page: next hop, peer CIDRs, warnings, copyable route hints, event history.
- *Delete* on active rows; *Cancel* on own pending rows; *Repair* on degraded rows (§7.4).

The plugin uses the token in the Horizon session and finds the API with `url_for(request, 'router-peering')`.

### 4.4 CLI (OSC plugin)

```
openstack router peering create --router <router> --peer-project <project-id> <name>
openstack router peering list [--incoming | --outgoing]
openstack router peering show <rpx-id>
openstack router peering accept --router <router> <rpx-id>
openstack router peering reject <rpx-id>
openstack router peering repair <rpx-id>
openstack router peering delete <rpx-id>      # also cancels a pending request it owns
```

## 5. Architecture

```
 Horizon panel ─┐                                   ┌─► Keystone (token check, project names)
 OSC plugin ────┼─► HAProxy VIP ─► router-peering-api ─┤
 REST clients ──┘   (Kolla)        (1 per host)     └─► Postgres (own container)
                                         │
                              router-peering-reconciler ─► Neutron (service admin account)
                                   (1 per host)
```

- **API**: stateless. Validates the user token, checks roles, reads and writes rows. **Never calls Neutron
  with write intent.** It reads Neutron (with the service account) only to validate router ownership and
  compute `peer_cidrs` / `warnings`.
- **Reconciler**: does all Neutron writes. Several copies may run; row locking keeps them apart (§7.1).
- API and reconciler ship as one binary with subcommands `api`, `reconciler`, `migrate`.
- **Data path independence**: an `active` peering is plain Neutron router ports. If API, reconciler or
  database is down, existing peerings keep forwarding traffic; only changes stop.

## 6. Data model (Postgres)

### 6.1 Tables

```sql
CREATE TYPE peering_status AS ENUM (
  'pending_acceptance','provisioning','active','degraded',
  'rejected','cancelled','expired','failed','deleting','deleted');

CREATE TABLE peerings (
  id                    text PRIMARY KEY,              -- 'rpx-' || 12 hex; regenerate on clash
  name                  text NOT NULL,
  status                peering_status NOT NULL DEFAULT 'pending_acceptance',
  status_reason         text,

  requester_project_id  varchar(64) NOT NULL,          -- Keystone id: 32 hex, no dashes
  requester_router_id   uuid NOT NULL,
  accepter_project_id   varchar(64) NOT NULL,
  accepter_router_id    uuid,                          -- set on accept

  transit_cidr          cidr,                          -- set by reconciler
  transit_network_id    uuid,
  transit_subnet_id     uuid,
  requester_port_id     uuid,
  accepter_port_id      uuid,
  requester_ip          inet,                          -- accepter's next hop
  accepter_ip           inet,                          -- requester's next hop

  attempts              int NOT NULL DEFAULT 0,
  next_attempt_at       timestamptz NOT NULL DEFAULT now(),
  claimed_by            text,                          -- reconciler instance id
  claimed_until         timestamptz,                   -- lease end; NULL or past = free
  warnings              jsonb NOT NULL DEFAULT '[]',   -- overlap found after accept (§9)

  created_by            varchar(64) NOT NULL,          -- Keystone user ids
  accepted_by           varchar(64),
  deleted_by            varchar(64),
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  expires_at            timestamptz NOT NULL DEFAULT now() + interval '7 days',
  deleted_at            timestamptz,

  CHECK (requester_project_id <> accepter_project_id)
);

-- one live peering per router pair, either direction
CREATE UNIQUE INDEX peerings_router_pair ON peerings
  (LEAST(requester_router_id, accepter_router_id), GREATEST(requester_router_id, accepter_router_id))
  WHERE accepter_router_id IS NOT NULL
    AND status NOT IN ('rejected','cancelled','expired','failed','deleted');

CREATE INDEX ON peerings (requester_project_id, status);
CREATE INDEX ON peerings (accepter_project_id, status);
CREATE INDEX ON peerings (next_attempt_at) WHERE status IN ('provisioning','deleting','active','degraded');

CREATE TABLE transit_blocks (
  cidr        cidr PRIMARY KEY,                        -- one /29
  peering_id  text UNIQUE REFERENCES peerings(id),     -- NULL = free
  reserved    boolean NOT NULL DEFAULT false           -- overlaps an existing subnet (§6.3)
);

CREATE TABLE peering_events (
  id                bigserial PRIMARY KEY,
  peering_id        text NOT NULL REFERENCES peerings(id),
  at                timestamptz NOT NULL DEFAULT now(),
  actor_user_id     varchar(64),                       -- NULL = reconciler
  actor_project_id  varchar(64),
  action            text NOT NULL,
  detail            jsonb
);
```

`peering_events.action` values: `requested`, `accepted`, `rejected`, `cancelled`, `expired`,
`provision_step`, `provisioned`, `degraded`, `repair_requested`, `repaired`, `delete_requested`,
`delete_blocked`, `deleted`, `project_gone`, `error`.

The router-pair index only covers rows with an accepter router, because Postgres `LEAST` / `GREATEST`
skip NULLs: without the `IS NOT NULL` filter, 2 pending requests from the same router would clash.
So the check applies when B accepts, and accept must catch the unique violation and return 409.

### 6.2 State machine

```
pending_acceptance ──accept──► provisioning ──ok──► active ──interface gone──► degraded
   │      │      │                  ▲  │                                         │
   │      │      │                  │  └─hard error─► failed                     │
   │      │      │                  └────────────── repair ──────────────────────┘
   │      │      └─7 days─► expired
   │      └─reject─► rejected
   └─cancel (requester)─► cancelled

active / degraded ──delete (either side) or project gone──► deleting ──► deleted
```

Terminal states: `rejected`, `cancelled`, `expired`, `failed`, `deleted`. Rows are kept for audit; a
retention purge is left to the builder.

### 6.3 Transit pool

- Configurable, default `100.64.0.0/16` → 8,192 blocks of /29. Each block uses 2 IPs:
  first usable = requester, second usable = accepter.
- Seeded at first start (idempotent). Changing the pool later only adds blocks; it never removes held ones.
- **Pre-flight overlap scan** **(call)**: Neutron accepts overlapping subnets on different networks, so it
  will not warn us. At seed time and then every hour, the reconciler lists all subnets in the cloud
  (service account) and marks any block that overlaps one as `reserved`, so it is never handed out.
  Neutron still checks overlap at `add_router_interface`, but only against that router's own subnets.
- **Known limit**: if a user later adds a subnet that overlaps a held /29 to a peered router, Neutron
  rejects it with a 400 the user may not understand. Documented; the scan keeps new blocks clear.
- Allocation, safe with many reconcilers:

```sql
UPDATE transit_blocks SET peering_id = $1
WHERE cidr = (SELECT cidr FROM transit_blocks WHERE peering_id IS NULL AND NOT reserved
              ORDER BY cidr LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING cidr;
```

- A block is freed only after its Neutron subnet is deleted.

## 7. Neutron operations

All Neutron writes use the service account: user `router-peering`, role `admin` in project `service`.
Every created object gets tag `rpx-<id>`.

### 7.1 Reconciler loop

Every 10 s **(call)**, each reconciler claims work with a **lease**, because one provision is 8 slow
Neutron calls and must not hold a database transaction open:

```sql
UPDATE peerings SET claimed_by = $me, claimed_until = now() + interval '2 minutes'
WHERE id IN (SELECT id FROM peerings
             WHERE status IN ('provisioning','deleting') AND next_attempt_at <= now()
               AND (claimed_until IS NULL OR claimed_until < now())
             ORDER BY next_attempt_at LIMIT 10 FOR UPDATE SKIP LOCKED)
RETURNING id;
```

The worker extends the lease after each step and clears it when done. Every status change is written
with `WHERE claimed_by = $me`, so a worker whose lease ran out cannot overwrite a newer one. Steps are
idempotent, so 2 workers briefly overlapping after a lease timeout do no harm.

It also runs, less often:
- **Expiry** (every minute): `pending_acceptance` past `expires_at` → `expired`.
- **Drift check** (every 5 min): for `active` / `degraded` rows, both ports exist and are attached to the
  recorded routers (§7.4), and both projects still exist in Keystone (§7.5).
- **Orphan sweep** (every hour): list the `service` project's networks and filter **client-side** for
  names / tags starting with `rpx-` (Neutron tag filters match exact tags only, no wildcards). Any with
  no non-terminal row, older than 1 hour → run the delete sequence on them.
- **Pool scan** (every hour): §6.3.

Every step **looks up by tag or stored ID first** and creates only when nothing exists. A crash between
any 2 steps resumes cleanly. Retry backoff: exponential from 5 s, capped at 5 min **(call)**.

### 7.2 Provision (`provisioning`)

| # | Step | Call | Notes |
|---|---|---|---|
| 0 | Re-check | `GET /v2.0/routers/{A}`, `/{B}` | Exist, owners still match. Else hard error. |
| 1 | Allocate block | SQL above | Store `transit_cidr`, `requester_ip`, `accepter_ip`. |
| 2 | Network | `POST /v2.0/networks` `{name: rpx-<id>, port_security_enabled: false, tags: [rpx-<id>]}` | Owned by `service`. Not shared, no RBAC. Default tenant network type. |
| 3 | Subnet | `POST /v2.0/subnets` `{network_id, cidr, ip_version: 4, gateway_ip: null, enable_dhcp: false, name: rpx-<id>}` | No gateway and no DHCP: only router ports live here. |
| 4 | Port A | `POST /v2.0/ports` `{network_id, project_id: A, name: rpx-<id>-requester, fixed_ips: [{subnet_id, ip_address: requester_ip}], tags: [rpx-<id>]}` | Owned by A: visible to A, counts against A's port quota. |
| 5 | Attach A | `PUT /v2.0/routers/{A}/add_router_interface` `{port_id}` | By port: the subnet has no gateway, and 2 routers share it. |
| 6 | Port B | as step 4, `project_id: B`, `accepter_ip`, name `-accepter` | |
| 7 | Attach B | as step 5 | |
| 8 | Done | `GET /v2.0/ports?tags=rpx-<id>` | Both have `device_owner = network:router_interface` and the right `device_id`. Then `active`. |

Tags on create need the `tag-creation` extension. Without it, steps 2, 3, 4 and 6 each set the tag right
after create with `PUT /v2.0/{networks,subnets,ports}/{id}/tags/rpx-<id>`. Lookups by stored ID cover
the gap between create and tag.

Errors:
- **Transient** (5xx, timeout, connection error, 409 on a busy router): retry the same step with backoff.
- **Hard** (router gone, owner changed, 400 overlap with a subnet already on that router, 403/404 on
  our own objects, quota exceeded on A or B's ports): run §7.3 on whatever exists, free the block, set `failed` with Neutron's message in
  `status_reason`.

### 7.3 Delete (`deleting`)

| # | Step | Call | Notes |
|---|---|---|---|
| 1 | Detach B | `PUT /v2.0/routers/{B}/remove_router_interface` `{port_id}` | Removes and deletes the port. 404 = already gone, continue. |
| 2 | Detach A | same for A | |
| 3 | Leftover ports | `DELETE` any port still tagged `rpx-<id>` | Covers the degraded case. |
| 4 | Subnet, network | `DELETE /v2.0/subnets/{id}`, `DELETE /v2.0/networks/{id}` | |
| 5 | Free block | `UPDATE transit_blocks SET peering_id = NULL WHERE peering_id = $1` | Only after step 4. |
| 6 | Done | `status = deleted`, `deleted_at`, `deleted_by` | |

If a user route still uses the next hop, Neutron answers 409 (router interface in use by a route). The
peering stays `deleting`, `status_reason` names the router and the route, an event `delete_blocked` is
written, and the reconciler retries with the capped backoff until the route is gone.

### 7.4 Drift, `degraded` and repair

**Verified on the test cloud (2026.1, ML2/OVN, 25/10):** a tenant **cannot** remove its peering interface.
`remove_router_interface` as the tenant answers `404 NetworkNotFound`, because the transit network
belongs to the `service` project and is invisible to the tenant. Deleting the port directly is refused
too, because Neutron only lets a router interface port be removed through the router. So `degraded`
happens only through an admin action or a Neutron-side fault, and repair is an operator tool rather
than an everyday user action.

- The drift check marks the peering `degraded` with a reason naming the missing side.
- The service **does not auto-repair**, because the removal may be deliberate.
- **Who**: a member of either project (Horizon *Repair* button, or `openstack router peering repair <id>`),
  or a cloud admin. **What runs**: the reconciler, with the service account.
- Either side may call `POST /v1/peerings/{id}/repair`. That sets `provisioning`; the provision steps
  find the existing network, subnet and surviving port by ID, and recreate only the missing port and
  attachment. The block and IPs stay the same, so user routes stay valid.
- Either side may delete instead.

### 7.5 Project deleted or disabled

Neutron does not cascade a Keystone project delete into our objects.
- **Accept** checks that the requester project still exists and is enabled.
- The drift check looks both projects up. If one is **deleted**, the peering moves to `deleting`
  (event `project_gone`) and is cleaned up like a normal delete. If one is **disabled**, nothing
  changes: traffic is the owner's problem, and a re-enabled project should find its peering intact.
- A `pending_acceptance` row whose project is deleted moves to `cancelled`.

### 7.6 Side effects

- **A peered router cannot be deleted.** Neutron blocks deleting a router that has interfaces, so users
  must delete the peering first.
- **No SNAT between peers.** OVN SNATs only traffic leaving the router's external gateway port.
- **Peering a router stops its owner adding a subnet that overlaps a peer.** Not enforced by Neutron;
  the drift check records it as a warning (§9).
- **Network and subnet quota** are charged to the `service` project. Raise it to the expected number of
  peerings (or unlimited).

## 8. API

Base path `/v1`. JSON. Auth header `X-Auth-Token` (Keystone v3, project-scoped).

| Method | Path | Who | Result |
|---|---|---|---|
| `POST` | `/v1/peerings` | member of the requester project | 201, `pending_acceptance` |
| `GET` | `/v1/peerings?direction=incoming\|outgoing&status=` | member or reader of either side | list, paginated (`limit`, `marker`) |
| `GET` | `/v1/peerings/{id}` | member or reader of either side | one item, per-caller view (§4.2) |
| `POST` | `/v1/peerings/{id}/accept` (no body) | member of the accepter project | 202, `provisioning` |
| `POST` | `/v1/peerings/{id}/reject` | member of the accepter project | 200, `rejected` |
| `POST` | `/v1/peerings/{id}/repair` | member of either side, only when `degraded` | 202, `provisioning` |
| `DELETE` | `/v1/peerings/{id}` | member of either side | 202 `deleting`, or 200 `cancelled` if requester and still pending |
| `GET` | `/v1/peerings/{id}/events` | member or reader of either side | audit list |
| `GET` | `/healthz`, `/readyz` | anyone | liveness, DB + Keystone reachability |

Rules:
- The token's **scoped project** decides the role in the request: a user in both A and B acts as
  whichever project the token is scoped to.
- Roles are **configurable per action**, defaults: `request_role`, `accept_role`, `reject_role`,
  `repair_role`, `delete_role` = `member`; `view_role` = `reader`. Keystone's default roles imply each
  other (`admin` ⊃ `member` ⊃ `reader`), so `admin` holders pass too. A stricter cloud can set
  `manager` or another role. A cloud admin (`admin` role on the cloud admin project) can list and
  delete any peering.
- `POST /v1/peerings` body: `{name, router_id, peer_project_id, peer_router_id}`. Checks: router exists and belongs to
  the token's project; peer project exists and differs; per-project limit of **50 non-terminal
  peerings** (configurable) **(call)**.
- Accept checks: router belongs to the token's project; not already peered with the requester router.
- Errors: `400` bad input, `403` wrong project or role, `404` unknown peering (also for peerings the
  caller is not part of), `409` state conflict or duplicate pair. Overlapping CIDRs are not an error
  (§9, warnings only).
- Token validation results are cached for at most 5 minutes, keyed by token hash **(call)**.

## 9. Overlapping CIDRs

**Rejected at accept.** Accept fails with `409` and lists the overlapping CIDRs if any of these overlap:

| Side 1 | Side 2 |
|---|---|
| subnets on A's router | subnets on B's router |
| subnets on A's router | subnets on routers already peered with B (non-terminal peerings) |
| subnets on B's router | subnets on routers already peered with A |

Transit subnets (`rpx-*`) are left out of the comparison. Nothing is built, and the request stays
`pending_acceptance`, so B can pick another router or A can change its subnets.

**After accept**, a user can still add an overlapping subnet with a plain Neutron call that we cannot
stop. The drift check finds it and stores it in `warnings`. The status stays `active`; nothing is
removed.

## 10. Deployment

All files live in the repo under `deploy/kolla/`, as examples for any Kolla cloud.

| Piece | How |
|---|---|
| Images | `router-peering` (API + reconciler) and `horizon-router-peering` (thin layer `FROM` the Kolla Horizon image of the cloud's release). Published by CI. |
| Postgres | One container on one controller, host volume, nightly `pg_dump` next to the Kolla MariaDB dumps. Not HA in v1. |
| API + reconciler | One container each per controller (or one combined container), compose file, `restart: always`. |
| HAProxy (optional) | Only for HA. `/etc/kolla/config/haproxy/services.d/router-peering.cfg` with a `listen` block on the internal VIP (and external if enabled), then `kolla-ansible reconfigure --tags haproxy`. |
| Port | Configurable. Must avoid Kolla's port list; the default is chosen during implementation. |
| Keystone | `deploy/kolla/register.sh`: create user `router-peering`, add `admin` on `service`, create service type `router-peering`, create public / internal / admin endpoints. Idempotent. |
| Horizon | Set `horizon_image` / `horizon_tag` in `globals.yml` to the extended image, then `kolla-ansible deploy --tags horizon`. Rebuild on every OpenStack upgrade. |
| Config | One file: Keystone auth for the service account, DB URL, transit pool, port, intervals, limits. Secrets by env or a mounted file, never in the image. |

## 11. Repo and languages

- **Backend: Go** (gophercloud for Neutron and Keystone, plain SQL migrations).
- **Horizon plugin and OSC plugin: Python**, as both must be. Published to PyPI as
  `router-peering-dashboard` and `python-routerpeeringclient`.
- License: **Apache-2.0**, the OpenStack ecosystem norm.
- One repo, `openstack-router-peering`; each part has its own release:

```
openstack-router-peering/
├── cmd/router-peering/main.go     # one binary: api | reconciler | migrate
├── internal/
│   ├── api/                       # HTTP handlers, per-caller view, role checks
│   ├── reconciler/                # provision, delete, repair, drift, expiry, orphan sweep, pool scan
│   ├── neutron/                   # thin gophercloud wrapper (only the calls in §7)
│   ├── keystone/                  # token validation + cache, project lookup
│   ├── store/                     # SQL, block allocator, leases
│   ├── model/                     # Peering, status enum, transitions
│   └── config/
├── migrations/0001_init.sql
├── dashboard/                     # Python, Horizon plugin
│   └── router_peering_dashboard/  #   panel, views, forms, templates, enabled/_1490_router_peering.py
├── client/                        # Python, OSC plugin
├── deploy/kolla/
│   ├── docker-compose.yml         # postgres + api + reconciler
│   ├── haproxy/router-peering.cfg # copy to /etc/kolla/config/haproxy/services.d/
│   ├── horizon/Dockerfile         # FROM the Kolla Horizon image + dashboard
│   ├── register.sh                # Keystone user, service, endpoints
│   └── router-peering.yaml.example
├── test/e2e/                      # real cloud: DevStack in CI, the test cloud by hand
├── docs/
├── Dockerfile
└── go.mod
```

## 12. Testing

Left to the builder, with 2 required gates:
- Unit tests for the state machine, the per-caller view and the block allocator (concurrency test with
  several goroutines claiming blocks).
- An end-to-end test against a real Neutron (DevStack with OVN in CI, plus a manual run on the test cloud):
  create, accept, ping between 2 VMs after adding routes and security groups, delete blocked by a route,
  delete after removing it, cancel, reject, expire, degraded after a manual detach, orphan sweep.

## 13. Left to the builder

- Internal package layout, HTTP framework, logging format, metrics (Prometheus endpoint suggested).
- Migration tool, as long as migrations are plain SQL and run at startup.
- Pagination details, event retention, terminal-row purge.
- Horizon panel styling, as long as it follows Horizon's own look.
- The exact default API port.

## 14. Open before implementation

- Verify on the test cloud that `100.64.0.0/16` is unused (tenant subnets, Calico, external network). The
  pool scan (§6.3) protects against tenant subnets, but not against Calico or other non-Neutron users.
- Verify that admin can create a port with `project_id` of another project on a network owned by
  `service`, and attach it to that project's router (expected yes: the port and the router have the
  same owner). **Fallback** if Neutron refuses: create the port as the service project and attach as
  admin. The port then counts against the `service` quota, and tenants cannot detach it, so §7.4
  `degraded` mostly goes away. The rest of the design is unchanged.
