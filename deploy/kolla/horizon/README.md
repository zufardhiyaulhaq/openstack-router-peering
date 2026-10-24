# Horizon plugin on Kolla-Ansible

Adds **Project → Network → Router Peering**. The panel only appears when the `router-peering` service is in
the Keystone catalog (run `deploy/kolla/register.sh` first).

## Option A: mount into the stock Horizon container (recommended)

No custom image, so OpenStack upgrades need no extra build.

1. Copy the plugin to the controller(s) running Horizon:
   `rsync -a frontend/router_peering_dashboard/ <host>:/opt/router-peering/dashboard/router_peering_dashboard/`
2. Back up `/etc/kolla/globals.yml`, then add `globals-snippet.yml` to it.
3. Append the line from `_9999-custom-settings.py.snippet` to `/etc/kolla/config/horizon/_9999-custom-settings.py`.
4. `kolla-ansible reconfigure -i <inventory> --tags horizon` (restarts Horizon only).
5. Check: `curl -s -o /dev/null -w '%{http_code}' http://<horizon>/project/router_peering/` gives 302 (login), not 404.

Upgrade the plugin: copy the new files, bump the version line from step 3, rerun step 4.

Not an official Kolla feature: it relies on `horizon_extra_volumes`. Check it still works after each Kolla upgrade.

## Option B: custom Horizon image

Build `Dockerfile` here with `frontend/` as context, push it to your registry, and set
`horizon_image` / `horizon_tag` in `globals.yml`, then `kolla-ansible deploy --tags horizon`.
Rebuild for every OpenStack release **before** the Kolla upgrade, or Horizon loses the panel.
