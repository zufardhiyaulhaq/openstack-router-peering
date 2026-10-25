# Router Peering: name the peer router at request time

Date: 2026-10-25 · Status: **approved 25/10** · Changes: `2026-10-23-router-peering-design.md` (§4, §6, §8, §9)

Items marked **(call)** are the author's decisions, not the owner's.

## 1. Intent

Today the requester names only the peer **project**. The accepter later picks which of its routers to
connect. That is backwards from every public cloud: on AWS, GCP and Alibaba Cloud the requester names
the peer **VPC ID** up front, and the accepter only confirms. A router is our VPC, so the requester must
say which router it wants, and both sides must see exactly which 2 routers are being joined before
anything is built.

Two smaller fixes ride along:
- **Stop exposing the peer's subnets** (`peer_cidrs`). Users do not need them from us: the peer tells
  them, the same way it tells them its project ID and router ID. It also removes every Neutron call from
  the read path.
- **Batch subnet lookups.** The service reads a router's subnets with one `GET /subnets?id=…&id=…`. Past
  about 200 subnets the URL exceeds the usual 8 KB proxy limit and the call fails, which breaks accept,
  the drift warnings and the overlap check for that router.

## 2. Goals, non-goals, anti-goals

**Goals**
- `POST /v1/peerings` requires `peer_router_id`. The service checks that router exists **in the peer
  project** before creating the request.
- Accept is a confirmation of the 2 named routers. The accepter cannot choose or swap a router.
- Both sides see both router IDs from the moment the request exists.
- One live peering per router pair, enforced from request time (pending included), either direction.
- No Neutron call when listing or viewing peerings.
- Router subnet lookups work for any number of subnets.

**Non-goals**
- Listing the peer's routers for the requester (it cannot see them; the ID comes out of band).
- Router names: the API stores and shows IDs only. Horizon may show the caller's own router name.
- Changing an existing peering's routers. To use other routers, delete and request again.

**Anti-goals (a failure even if it works)**
- The requester learns anything about the peer beyond "this project exists" and "this router exists in
  that project" before the peer accepts. In particular it must never see the peer's subnets.
- An accepter accepts a peering onto a router it did not see in the request.

## 3. Decisions

| # | Decision | Source |
|---|---|---|
| D1 | Request body: `{name, router_id, peer_project_id, peer_router_id}`, all required | owner |
| D2 | At request: peer router missing, or owned by another project → **404 "router not found in peer project"** (existence only, same as for the project ID) | owner (table, 25/10) |
| D3 | Accept takes **no router**; see D13 for a body that names one | owner (table) |
| D4 | Accept re-checks the accepter's router: deleted or moved to another project → **409 "your router no longer exists"** | owner (table) |
| D5 | Overlap check **stays at accept**, not request: a 409 at request time would show the requester the peer's subnets (anti-goal) | owner (table) |
| D6 | Same router pair, either direction, while a peering is live (any non-terminal status, pending included) → **409 at request** | owner (table) |
| D7 | `peer_cidrs` removed from every API response and from Horizon | owner |
| D8 | Route hint becomes one line with a placeholder: `openstack router set --route destination=<peer subnet>,gateway=<next hop> <your router>` | owner (proposal accepted) |
| D9 | Subnet lookups in batches of **50** IDs per request **(call)** | owner asked for the fix; batch size is the author's |
| D11 | Pending rows created before this change have no `accepter_router_id`. Accept on such a row → **409 "this request names no peer router; ask the requester to request again"** **(call)**. the test cloud has none. | author |
| D12 | **Drift cancels a pending request** when either named router is gone (deleted, or now owned by another project): `cancelled`, reason "router `<id>` no longer exists", event `project_gone`-style event `router_gone` | owner (Q1) |
| D13 | Accept with a body naming a **different** router → **400 "this peering is for router `<id>`; accept takes no router"**. Empty body or the same router → accepted. (Replaces "any body is ignored" in D3.) | owner (Q2) |
| D14 | Loose **UUID format check** on `router_id` and `peer_router_id` → **400 "… is not a valid router ID"**, before any Neutron call | owner (Q3) |
| D15 | The accepter sees the requester's router ID (`peer_router_id`) while the request is pending | owner (Q4) |
| D16 | Horizon shows **only live peerings** (pending, provisioning, active, degraded, deleting) in Outgoing and Incoming; finished ones (rejected, cancelled, expired, failed, deleted) move to a third tab **History** (both directions, read-only) **(call: a tab rather than a toggle)**. The API is unchanged: it already filters with `?status=`. | owner (Q6) |
| D10 | **No DB migration** (owner, Q5): `accepter_router_id` stays nullable so rows created before this change still load; the API now always fills it at create. The existing partial unique index (`… WHERE accepter_router_id IS NOT NULL AND status NOT IN (terminal)`) then covers pending requests automatically. | author |

Facts the builder can rely on (checked against the existing design and code):
- The router-pair index is `(LEAST(requester_router_id, accepter_router_id), GREATEST(…))`, so A→B and
  B→A are the same pair (D6 needs no new index).
- `next_hop` comes from the stored `requester_ip` / `accepter_ip`, set at provisioning; GET needs no
  Neutron call to show it.
- Every Neutron read and write, including reading a router in another project, uses the service's own
  admin account, never the caller's token.

## 4. API changes (spec §8)

| Endpoint | Before | After |
|---|---|---|
| `POST /v1/peerings` | `{name, router_id, peer_project_id}` | adds required `peer_router_id`; new errors: 400 missing, 404 not in peer project, 409 same pair already live |
| `POST /v1/peerings/{id}/accept` | `{router_id}` required | no body; new error 409 "your router no longer exists" |
| `GET /v1/peerings`, `GET /v1/peerings/{id}` | `peer_cidrs`, hints per CIDR; GET read the peer router from Neutron | no `peer_cidrs`; new `peer_router_id`; one placeholder hint; **no Neutron call** |

Check order on `POST /v1/peerings` (first failure wins): role → body → name → peer project ID format →
not own project → own `router_id` given and UUID-shaped → `peer_router_id` given and UUID-shaped (D14) → peer project exists (404) → own router
in own project (400) → peer router in peer project (404) → open-peering limit (409) → same pair live (409).

Check order on accept: visible to caller (404) → role (403) → caller is accepter (403) → body names a
different router (400, D13) → status pending (409) → request has a peer router (409, D11) → requester project exists and enabled (409) → **accepter router exists and still owned (409)** →
requester router exists and still owned (409) → overlap (409 with `overlaps`) → state change.

### Per-caller view (spec §4.2)

| Field | Requester sees | Accepter sees |
|---|---|---|
| `router_id` | its own router | its own router (**set from the start**, was empty until accept) |
| `peer_router_id` | the router it asked for | the requester's router |
| `peer_project_id` / `peer_project_name` | ID only | ID + name |
| `next_hop`, `route_hints` | after active: the peer's transit IP, 1 placeholder hint | the mirror |
| `peer_cidrs` | removed | removed |

## 5. Horizon plugin changes (spec §4.3)

| Screen | Change |
|---|---|
| Request Peering | new required field **Peer router ID**, help text "Ask the other project for the router ID they want to peer" |
| Accept | no router dropdown; read-only **Your router: `<name>` (`<id>`)** (name read with Horizon's own `router_get`, the accepter owns it; ID only if that fails) and **Requested by**; buttons Accept / Cancel |
| List | Outgoing and Incoming show **live peerings only**; new tab **History** for finished ones (D16); new column **Peer Router**; "My Router" is now filled on incoming pending rows |
| Detail | drop **Peer CIDRs**; add **Peer router ID**; route hint shows the placeholder line |

## 6. Rollout and compatibility

- The request and accept contracts change, so **backend and Horizon plugin ship together** (one
  deploy window). An old plugin against the new backend fails at request (400, no `peer_router_id`);
  accept still works because the body is ignored.
- Existing peerings are unaffected: the only live one (`rpx-0123456789ab`) already has both routers.
- No other client exists yet (the CLI plugin is not built).
- The e2e test sends `peer_router_id` and an empty accept; rerun it on the test cloud after the deploy.

## 7. Tests the change must add or update

- Request: missing `peer_router_id` (400); peer router in a third project (404); peer router missing
  (404); same pair again (409); reverse direction of the same pair (409).
- Accept: body with another router → 400 (D13); empty body or the same router → 202; accepter router
  deleted while pending (409 "your router no longer exists"); legacy row without a peer router (409, D11).
- Request: malformed `router_id` / `peer_router_id` → 400 with no Neutron call (D14).
- Drift: pending request whose either router is deleted → `cancelled` with reason (D12).
- Horizon: Outgoing/Incoming call the API with live statuses only; History lists finished ones (D16).
- View: `peer_router_id` correct for both sides; no `peer_cidrs` key; one placeholder hint.
- GET performs no Neutron call (inject a Neutron failure; GET still 200).
- Store: create stores `accepter_router_id`; second live peering on the same pair → conflict.
- Neutron client: a router with 120 subnets is read in 3 lookups of at most 50 IDs.
- Horizon: create posts `peer_router_id`; accept form shows "Your router" and posts nothing else;
  detail has no Peer CIDRs.

## 8. Left to the builder

- Exact wording of new error messages beyond the ones quoted above.
- Column order in the Horizon tables.

## 9. State of the code

Built ahead of this spec on branch `feat/peer-router` (owner asked to pause): `74c6446` (batching) and
`915e98a` (API and e2e). The Horizon part is not done. After approval, the plan checks both commits
against this spec and fixes any difference.
