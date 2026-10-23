# Router peering for OpenStack: brainstorm notes

> Superseded by `docs/superpowers/specs/2026-10-23-router-peering-design.md`. Kept for history.

Status: **brainstorm, not an approved design.** Items marked *(guess)* or *(verify)* are open.

## What it is

An open source service that adds AWS / GCP / Alibaba Cloud style VPC peering to OpenStack.
Project A asks to peer one of its routers with project B. Project B accepts and picks its own router.
The service then connects the 2 routers through a private transit network and stops there.
Users add their own static routes and security group rules, the same as on AWS.

First target: the test cloud (Kolla-Ansible 2026.1, Neutron ML2/OVN, 5 hosts).

## Decisions so far

| Topic | Decision |
|---|---|
| Scope of automation | Steps 1-2 only: transit network + both router interfaces. No routes, no security groups. |
| Approval | Requester asks, accepter accepts or rejects. Pending requests expire after 7 days. |
| Peer identity | Requester enters the **accepter's Keystone project ID**. It cannot list or look up other projects. |
| What each side sees | Requester sees only the peer project ID. Accepter sees the requester's project name + ID. |
| User CIDR input | None. The service picks a transit /29 from a reserved pool. |
| Transit network naming | `rpx-<id>` for network, subnet and ports, plus Neutron tag `rpx-<id>`. |
| Network sharing | **No RBAC share.** Ports are created by the service, so tenants cannot boot VMs on transit. |
| State | Own backend + own Postgres. Neutron alone cannot hold pending requests or history. |
| Runtime | Containers on the Kolla hosts (`docker run` / compose), not k8s. Kolla does not manage them. |
| API exposure | Kolla HAProxy VIP via `/etc/kolla/config/haproxy/services.d/`, Keystone catalog type `router-peering`. |
| Frontend | Horizon plugin (Project > Network > Router Peering) + an OSC plugin (`openstack router peering ...`). |
| Auth | User Keystone token, validated by the backend. Neutron calls use the service's own admin account. |

## Data structure

### Entities

```
peerings 1 ──── 0..1 transit_blocks      (a block is held while the peering has Neutron resources)
peerings 1 ──── n    peering_events      (audit trail, append only)
```

Neutron objects are not copied into the DB. The DB stores their IDs, and every Neutron object carries
the tag `rpx-<id>`, so each side can be checked against the other.

### `peerings`

| Column | Type | Set when | Meaning |
|---|---|---|---|
| `id` | text PK | request | `rpx-` + 12 hex. Retry on a unique clash. |
| `name` | text | request | Free text from the requester. |
| `status` | enum | always | See state machine below. |
| `status_reason` | text | on error / block | Human text, e.g. "remove routes via 100.64.0.10 first". |
| `requester_project_id` | varchar(64) | request | Keystone project ID (32 hex, no dashes). |
| `requester_router_id` | uuid | request | Must be owned by `requester_project_id`. |
| `accepter_project_id` | varchar(64) | request | Typed by the requester; checked to exist. |
| `accepter_router_id` | uuid | accept | Must be owned by `accepter_project_id`. |
| `transit_cidr` | cidr | provisioning | The /29 from `transit_blocks`. |
| `transit_network_id` | uuid | provisioning | Neutron network `rpx-<id>`. |
| `transit_subnet_id` | uuid | provisioning | Neutron subnet `rpx-<id>`. |
| `requester_port_id` | uuid | provisioning | Router A's interface port. |
| `accepter_port_id` | uuid | provisioning | Router B's interface port. |
| `requester_ip` | inet | provisioning | First usable IP of the block. B's next hop to reach A. |
| `accepter_ip` | inet | provisioning | Second usable IP. A's next hop to reach B. |
| `attempts` | int | reconciler | Retries of the current step. |
| `next_attempt_at` | timestamptz | reconciler | Backoff. |
| `created_by` / `accepted_by` / `deleted_by` | varchar(64) | per action | Keystone user IDs. |
| `created_at` / `updated_at` / `expires_at` / `deleted_at` | timestamptz | per action | `expires_at` = created + 7 days. |

Constraints:
- `CHECK (requester_project_id <> accepter_project_id)`.
- One live peering per router pair, either direction: unique index on
  `(LEAST(requester_router_id, accepter_router_id), GREATEST(...))` where status is not `rejected`, `expired`, `deleted`.
- Indexes on `(requester_project_id, status)` and `(accepter_project_id, status)` for the 2 list tabs.

### State machine

```
             accept                 Neutron done
pending_acceptance ──────► provisioning ──────────► active
   │  │                        │                      │
   │  └─ reject ──► rejected   └─ hard error ──► failed (resources rolled back, block freed)
   └─ 7 days ──► expired                              │ delete (either side)
                                                      ▼
                                 deleting ──── Neutron cleaned ───► deleted
                                    └─ routes still use next hop: stays deleting, status_reason set
```

`degraded` *(guess)*: an `active` peering whose router interface was removed by hand. See open questions.

### `transit_blocks`

| Column | Type | Meaning |
|---|---|---|
| `cidr` | cidr PK | One /29. Seeded once: all 8,192 blocks of `100.64.0.0/16` *(verify the range is free on the test cloud)*. |
| `peering_id` | text, unique, FK | NULL = free. |

Allocation is one statement, safe with several backend replicas:

```sql
UPDATE transit_blocks SET peering_id = $1
WHERE cidr = (SELECT cidr FROM transit_blocks WHERE peering_id IS NULL
              ORDER BY cidr LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING cidr;
```

A block is freed only after its Neutron subnet is gone, so a block is never reused while Neutron still holds it.

### `peering_events`

| Column | Type | Meaning |
|---|---|---|
| `id` | bigserial PK | |
| `peering_id` | text FK | |
| `at` | timestamptz | |
| `actor_user_id`, `actor_project_id` | varchar(64) | NULL = the reconciler. |
| `action` | text | `requested`, `accepted`, `rejected`, `expired`, `provision_step`, `provisioned`, `delete_requested`, `deleted`, `error`. |
| `detail` | jsonb | Step name, Neutron request ID, error body. |

### API view (per caller)

The same row is shown differently:

| Field | Requester sees | Accepter sees |
|---|---|---|
| peer | `peer_project_id` only | requester project **name** + ID (looked up live in Keystone) |
| my router | `requester_router_id` | `accepter_router_id` |
| `next_hop` | `accepter_ip` | `requester_ip` |
| `peer_cidrs` | B router's subnets (info only) | A router's subnets (info only) |
| `route_hint` | `openstack router set --route destination=<B cidr>,gateway=<accepter_ip> <router>` | the mirror |

## How the backend creates the network

All Neutron calls use the service account (`admin` in project `service`). The API only writes rows;
**a reconciler loop does all Neutron work.** Accept just sets `status = provisioning`.

The reconciler picks work with `SELECT ... WHERE status IN ('provisioning','deleting') AND next_attempt_at <= now()
FOR UPDATE SKIP LOCKED`, so one backend container per host is safe. Every step first **looks up by tag**
and only creates when nothing is found, so a crash between steps just resumes.

### Provision (status `provisioning`)

| # | Step | Neutron call | Notes |
|---|---|---|---|
| 0 | Re-check | `GET /routers/{A}`, `GET /routers/{B}` | Both exist, owners match the projects. Else `failed`. |
| 1 | Allocate block | DB statement above | e.g. `100.64.0.8/29`. Store in `transit_cidr`. |
| 2 | Network | `POST /networks {name: rpx-<id>, port_security_enabled: false, tags: [rpx-<id>]}` | Owned by the service project. Not shared. Default tenant type (Geneve). Tags on create need the `tag-creation` extension; else `PUT /networks/{id}/tags/rpx-<id>`. |
| 3 | Subnet | `POST /subnets {network_id, cidr: 100.64.0.8/29, ip_version: 4, gateway_ip: null, enable_dhcp: false, name: rpx-<id>}` | No gateway, no DHCP: only 2 router ports live here. |
| 4 | Port A | `POST /ports {network_id, project_id: <A>, name: rpx-<id>-requester, fixed_ips: [{subnet_id, ip_address: 100.64.0.9}], tags: [rpx-<id>]}` | Fixed IPs make the next hops predictable. `project_id` = A, so A sees it and it counts against A's port quota. |
| 5 | Attach A | `PUT /routers/{A}/add_router_interface {port_id}` | By **port**, not by subnet: a subnet gateway can sit on only one router, and this subnet has none. Neutron sets `device_owner = network:router_interface`. |
| 6 | Port B | same as 4 with `project_id: <B>`, IP `100.64.0.10` | |
| 7 | Attach B | `PUT /routers/{B}/add_router_interface {port_id}` | |
| 8 | Check | `GET /ports?tags=rpx-<id>` | Both ports bound to their routers. Then `status = active`. |

Failure rules:
- **Transient** (5xx, timeout, 409 on a busy router): back off and retry the same step.
- **Hard** (router gone, owner changed, Neutron 400 "subnet overlaps with another subnet on the router"):
  run the delete sequence below, free the block, set `status = failed` with the reason.

### Delete (status `deleting`, from either side)

| # | Step | Neutron call | Notes |
|---|---|---|---|
| 1 | Detach B | `PUT /routers/{B}/remove_router_interface {port_id}` | Removing by port also deletes the port. |
| 2 | Detach A | same for A | |
| 3 | Subnet + network | `DELETE /subnets/{id}`, `DELETE /networks/{id}` | |
| 4 | Free block | `UPDATE transit_blocks SET peering_id = NULL` | Only after step 3 succeeds. |
| 5 | Done | `status = deleted`, `deleted_at = now()` | Row is kept for audit. |

If a user still has a route via the next hop, Neutron refuses step 1 or 2 with 409
`RouterInterfaceInUseByRoute`. The peering stays `deleting`, `status_reason` names the route, and the
reconciler retries until the user removes it.

### Side effects worth knowing

- **Routers cannot be deleted while peered.** Neutron blocks router delete while interfaces exist,
  so a user must delete the peering first. Good property.
- **The transit pool must not overlap any tenant subnet.** If router A already has `100.64.0.0/24`,
  step 5 fails with an overlap error (hard error, `failed`).
- **No SNAT between peers.** OVN only SNATs traffic leaving the router's external gateway port.
- **Orphan sweep.** A periodic job lists Neutron networks tagged `rpx-*` with no live DB row and
  deletes them after a grace period.

## Open questions

1. A tenant owns its router interface port, so it can remove it by hand. Should the service mark the
   peering `degraded`, re-attach it, or ignore it? *(Alternative: ports owned by the service project;
   verify whether admin may attach a port from another project to a router.)*
2. Overlapping tenant CIDRs between A and B: warn only, or block like AWS?
3. Is `100.64.0.0/16` free on the test cloud (tenant subnets, Calico, external `192.168.x`)?
4. Can the requester cancel a `pending_acceptance` request? *(guess: yes, it moves to `deleted`.)*
5. Backend language: Go (gophercloud) or Python (openstacksdk, closer to Horizon/OSC plugins)?
6. Port for the API on the Kolla VIP (must avoid Kolla's port list).
