# End-to-end test

Runs the whole flow against a real cloud with router-peering deployed. It creates its own networks
(`10.91.0.0/24` in project A, `10.92.0.0/24` in project B), routers, a security group and 2 VMs, and
deletes them at the end. The 2 subnets must not overlap anything already on those projects' routers.

| Env | Meaning |
|---|---|
| `RP_E2E_API_URL` | router-peering API, e.g. `http://<host>:9797` |
| `RP_E2E_CLOUD_A`, `RP_E2E_CLOUD_B` | `clouds.yaml` entries for a `member` of 2 **different** projects |
| `RP_E2E_IMAGE`, `RP_E2E_FLAVOR` | image and flavor IDs; the image must run cloud-init and have `ping` |

```bash
cd backend && go test -tags e2e ./test/e2e/... -v -timeout 40m
```

What it checks, in order: create → accept → active; VM A pings VM B after both sides add routes;
delete is blocked while a route uses the next hop, then finishes; cancel; reject; a manually
detached interface turns the peering `degraded` (waits for the 5-minute drift check) and repair
brings it back. Expiry and the orphan sweep are covered by unit tests only.

## Runs

| Date | Cloud | Result |
|---|---|---|
| — | the test cloud | not run yet (needs the VPN, a deploy, and the 2 checks in the main README) |
