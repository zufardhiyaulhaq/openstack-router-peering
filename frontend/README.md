<div align="center">
  <img src="../docs/assets/logo.png" alt="Router Peering" width="110">

  <h1>Router Peering Dashboard</h1>

  <p><strong>The Horizon panel for Router Peering: request, accept and manage peerings without the CLI.</strong></p>

  <p>
    <a href="../LICENSE"><img alt="License" src="https://img.shields.io/badge/license-Apache%202.0-blue.svg"></a>
    <a href="https://github.com/zufardhiyaulhaq/openstack-router-peering/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/zufardhiyaulhaq/openstack-router-peering/actions/workflows/ci.yml/badge.svg"></a>
    <img alt="Horizon" src="https://img.shields.io/badge/Horizon-2026.1-red.svg">
    <img alt="Django" src="https://img.shields.io/badge/Django-4.2-green.svg">
  </p>

  <p>
    <a href="../README.md">Router Peering</a> ·
    <a href="../deploy/kolla/horizon/README.md">Deploy on Kolla</a> ·
    <a href="../docs/superpowers/specs/2026-10-23-router-peering-design.md">Design</a>
  </p>
</div>

---

`router-peering-dashboard` adds **Project → Network → Router Peering** to OpenStack Horizon. It calls the
Router Peering API with the user's own Horizon session token and finds the API in the Keystone catalog
(service type `router-peering`). The panel only appears when that service is in the catalog.

It is a classic server-rendered Django panel: no AngularJS and no custom JavaScript, so it ships almost
no static files.

## ✨ Features

- **Outgoing / Incoming / History tabs**: live peerings you requested, live peerings waiting on you, and
  finished ones (newest first).
- **Request Peering**: pick your router, enter the peer project ID and the peer router ID.
- **Accept / Reject**: accept shows the requester and *your* router that was requested; there is nothing
  to choose. Overlapping CIDRs are reported with the exact subnets.
- **Detail page**: status and reason, next hop, warnings, a copy-ready `openstack router set --route`
  command, and the full event history.
- **Row actions by state**: Cancel, Reject, Repair and Delete only show when the API would allow them.
- **Fails soft**: if the API is down, pages show an error message instead of a server error.

## 📦 Requirements

- Horizon 2026.1 (Django 4.2, Python 3.12). Older releases are untested.
- The Router Peering API registered in Keystone as service type `router-peering`.
- `requests` (already in every Horizon environment).

## 🚀 Installing

**On Kolla-Ansible** (recommended), mount the plugin into the stock Horizon container. No custom image is
needed; see [`deploy/kolla/horizon/README.md`](../deploy/kolla/horizon/README.md).

**Anywhere else**, install it into Horizon's Python environment and enable the panel:

```bash
pip install ./frontend
cp frontend/router_peering_dashboard/enabled/_1490_project_router_peering_panel.py \
   <horizon>/openstack_dashboard/local/enabled/
python manage.py collectstatic --noinput && python manage.py compress --force
```

Then restart Horizon.

## ⚙️ Configuration

None of its own. The panel uses Horizon's existing settings:

| Horizon setting | Effect |
|---|---|
| `OPENSTACK_ENDPOINT_TYPE` | Which catalog interface (public / internal) the panel calls |
| `OPENSTACK_SSL_NO_VERIFY`, `OPENSTACK_SSL_CACERT` | TLS verification for calls to the API |

API calls time out after 10 seconds and never follow redirects, so the user's token cannot leave the
catalog endpoint.

## 🧪 Running Tests

```bash
make test
```

The tests run inside the Kolla Horizon 2026.1 image (`quay.io/openstack.kolla/horizon:2026.1-ubuntu-noble`),
so they use exactly the Horizon, Django and Python versions the panel is deployed on. Docker is required.

## 📁 Layout

| Path | What |
|---|---|
| `router_peering_dashboard/api/peering.py` | API client (token, catalog URL, paging, errors) |
| `router_peering_dashboard/dashboards/project/router_peering/` | panel, tabs, tables, forms, views, templates |
| `router_peering_dashboard/enabled/` | Horizon enable file |
| `router_peering_dashboard/test/` | test settings and helpers |

## 📄 License

Apache-2.0.
