# router-peering-dashboard

Horizon plugin: **Project → Network → Router Peering**. It talks to the router-peering API (found in the
Keystone catalog as service type `router-peering`) with the user's own Horizon session token. The panel
is hidden when that service is not in the catalog.

- Code: `router_peering_dashboard/` (classic Django panel, no AngularJS).
- Enable file: `router_peering_dashboard/enabled/_1490_project_router_peering_panel.py`.
- Tests: `make test` (runs inside the Kolla Horizon 2026.1 image; needs Docker).
- Deploy on Kolla: see `deploy/kolla/horizon/` in the repo root.
