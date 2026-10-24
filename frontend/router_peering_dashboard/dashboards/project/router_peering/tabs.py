from django.contrib import messages
from django.utils.translation import gettext_lazy as _
from horizon import tabs

from router_peering_dashboard.api import peering as api
from router_peering_dashboard.dashboards.project.router_peering import tables


def _list(request, direction):
    try:
        return tables.wrap(api.list(request, direction=direction))
    except api.APIError as e:
        messages.error(request, e.message)
        return []


class OutgoingTab(tabs.TableTab):
    name = _("Outgoing")
    slug = 'outgoing'
    table_classes = (tables.OutgoingTable,)
    template_name = 'horizon/common/_detail_table.html'
    preload = True

    def get_outgoing_data(self):
        return _list(self.request, 'outgoing')


class IncomingTab(tabs.TableTab):
    name = _("Incoming")
    slug = 'incoming'
    table_classes = (tables.IncomingTable,)
    template_name = 'horizon/common/_detail_table.html'
    preload = True

    def get_incoming_data(self):
        return _list(self.request, 'incoming')


class PeeringTabs(tabs.TabGroup):
    slug = 'peering_tabs'
    tabs = (OutgoingTab, IncomingTab)
    sticky = True
