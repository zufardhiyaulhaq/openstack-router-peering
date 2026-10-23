#!/usr/bin/env bash
# Register router-peering in Keystone. Idempotent: safe to run again.
# Needs admin credentials loaded (e.g. source /etc/kolla/admin-openrc.sh) and:
#   RP_PASSWORD      password for the router-peering service user
#   RP_PUBLIC_URL    e.g. http://<host-or-vip>:9797
#   RP_INTERNAL_URL  e.g. http://<host-or-vip>:9797
#   RP_REGION        default RegionOne
set -euo pipefail

: "${RP_PASSWORD:?set RP_PASSWORD}"
: "${RP_PUBLIC_URL:?set RP_PUBLIC_URL}"
: "${RP_INTERNAL_URL:?set RP_INTERNAL_URL}"
REGION="${RP_REGION:-RegionOne}"

if ! openstack user show router-peering --domain Default >/dev/null 2>&1; then
  openstack user create --domain Default --password "$RP_PASSWORD" router-peering
fi
openstack role add --project service --user router-peering --user-domain Default admin

if ! openstack service show router-peering >/dev/null 2>&1; then
  openstack service create --name router-peering --description "Router peering" router-peering
fi

for iface in public internal admin; do
  url="$RP_INTERNAL_URL"
  [ "$iface" = public ] && url="$RP_PUBLIC_URL"
  if [ -z "$(openstack endpoint list --service router-peering --interface "$iface" --region "$REGION" -f value -c ID)" ]; then
    openstack endpoint create --region "$REGION" router-peering "$iface" "$url"
  fi
done

echo "router-peering registered. Raise the network/subnet quota of project 'service' to at least the number of peerings you expect."
