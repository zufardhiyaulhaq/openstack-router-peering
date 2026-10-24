from django.utils.translation import gettext_lazy as _
import horizon


class RouterPeering(horizon.Panel):
    name = _("Router Peering")
    slug = 'router_peering'
    # Shown only when the cloud has the router-peering service in its catalog.
    permissions = ('openstack.services.router-peering',)
