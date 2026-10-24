# Fallback when mounting is not wanted: the stock Kolla Horizon image plus the plugin.
# Build with the frontend/ directory as context:
#   docker build -f deploy/kolla/horizon/Dockerfile --build-arg RELEASE=2026.1 -t <registry>/horizon:2026.1-router-peering frontend
# Rebuild for every OpenStack release, before the Kolla upgrade.
ARG RELEASE=2026.1
FROM quay.io/openstack.kolla/horizon:${RELEASE}-ubuntu-noble
COPY . /tmp/router-peering-dashboard
RUN /var/lib/kolla/venv/bin/pip install --no-deps --no-cache-dir /tmp/router-peering-dashboard \
 && cp /tmp/router-peering-dashboard/router_peering_dashboard/enabled/_1490_project_router_peering_panel.py \
       /var/lib/kolla/venv/lib/python3/site-packages/openstack_dashboard/local/enabled/ \
 && rm -rf /tmp/router-peering-dashboard
