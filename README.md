# openstack-router-peering

VPC-style peering between OpenStack routers in different projects, with a request / accept flow like
AWS, GCP and Alibaba Cloud. Project A asks to peer one of its routers with project B. B accepts and picks
its own router. The service then connects the 2 routers through a private transit network and stops.
Users add their own routes and security group rules.

Design: [docs/superpowers/specs/2026-10-23-router-peering-design.md](docs/superpowers/specs/2026-10-23-router-peering-design.md).

## Flow

| # | Who | Action | Status after |
|---|---|---|---|
| 1 | Requester (project A) | Picks its router, enters B's project ID and a name | `pending_acceptance` |
| 2a | Accepter (project B) | Picks its own router and accepts. Refused (409) if the CIDRs overlap | `provisioning` → `active` |
| 2b | Accepter | Rejects | `rejected` |
| 2c | Requester | Cancels before B answers | `cancelled` |
| 2d | Nobody, 7 days | — | `expired` |
| 3 | Both users | Add routes via the `next_hop` shown on the peering, and security group rules | stays `active` |
| 4 | Either side | Deletes (blocked until routes via the next hop are removed) | `deleting` → `deleted` |

B finds its project ID with `openstack token issue -c project_id`.

## API

`X-Auth-Token` must be a project-scoped Keystone token.

| Method | Path |
|---|---|
| `POST` | `/v1/peerings` `{name, router_id, peer_project_id}` |
| `GET` | `/v1/peerings?direction=incoming\|outgoing&status=&limit=&marker=` (cloud admin: `&all_projects=true`) |
| `GET` | `/v1/peerings/{id}`, `/v1/peerings/{id}/events` |
| `POST` | `/v1/peerings/{id}/accept` `{router_id}`, `/reject`, `/repair` |
| `DELETE` | `/v1/peerings/{id}` |
| `GET` | `/healthz`, `/readyz` |

## Deploy on a Kolla-Ansible cloud

Everything runs as plain containers; Kolla does not manage them.

1. **Check the transit pool is free.** The default is `100.64.0.0/16`. No tenant subnet, Kubernetes CNI
   or provider network may use it. The service reserves blocks that overlap Neutron subnets, but it
   cannot see anything outside Neutron.
2. **Register in Keystone** (once, with admin credentials):
   ```bash
   RP_PASSWORD=... RP_PUBLIC_URL=http://<host>:9797 RP_INTERNAL_URL=http://<host>:9797 deploy/kolla/register.sh
   ```
   Raise the `service` project's network and subnet quota to the number of peerings you expect.
3. **Configure.** Copy `deploy/kolla/` to the host (e.g. `/opt/router-peering`), copy
   `router-peering.yaml.example` to `router-peering.yaml`, set `keystone.auth_url`, and write `.env` with
   `RP_DATABASE_URL=postgres://router_peering:<pw>@127.0.0.1:5433/router_peering?sslmode=disable`,
   `POSTGRES_PASSWORD=<pw>` and `RP_KEYSTONE_PASSWORD=<service user password>`.
4. **Start (standalone).** `docker compose up -d`. The catalog endpoints from step 2 point at this host.
5. **Optional, for HA.** Run `api` and `reconciler` on more hosts against the same database.
   - **First** set `listen: "<host api_interface IP>:9797"` in `router-peering.yaml` on every api host
     and restart it. With the default `:9797`, HAProxy cannot bind `<VIP>:9797` and stops, taking
     every OpenStack API down.
   - Set `RP_DB_LISTEN=<db host internal IP>` on the database host so remote api/reconciler containers
     can reach Postgres; it listens on loopback only by default.
   - Copy `haproxy/router-peering.cfg` to `/etc/kolla/config/haproxy/services.d/`, run
     `kolla-ansible reconfigure --tags haproxy`, and point the catalog endpoints at `http://<VIP>:9797`.
6. **Back up** with a nightly `pg_dump` of `router_peering`.

## Development

```bash
make test       # unit tests (DB tests skip)
make test-db    # starts postgres:16 in Docker and runs every test
make build
```

The end-to-end suite in `test/e2e/` runs against a real cloud; see its README.

CI (`.github/workflows/ci.yml`) runs vet and every test against Postgres on each push and PR. Each push
to `main` publishes `ghcr.io/zufardhiyaulhaq/router-peering` (amd64 + arm64) as `:latest` and
`:sha-<short>`; a `vX.Y.Z` tag also publishes `:X.Y.Z` and `:X.Y`. Pin a `sha-` or version tag in
production rather than `:latest`.

## Before the first production use

- Confirm the transit pool does not overlap anything in your cloud (step 1).
- Confirm Neutron lets the admin service account create a port owned by project A on the service's
  transit network and attach it to A's router (the e2e suite proves it).

## License

Apache-2.0.
