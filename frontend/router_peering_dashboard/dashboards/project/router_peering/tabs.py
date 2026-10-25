from django.contrib import messages
from django.utils.translation import gettext_lazy as _
from horizon import tabs

from router_peering_dashboard.api import peering as api
from router_peering_dashboard.dashboards.project.router_peering import tables


def _list(request, direction, statuses, newest=False):
    try:
        if newest:
            return tables.wrap(api.list(request, direction=direction, statuses=statuses, newest=True))
        return tables.wrap(api.list(request, direction=direction, statuses=statuses))
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
        return _list(self.request, 'outgoing', tables.SHOWN_LIVE)


class IncomingTab(tabs.TableTab):
    name = _("Incoming")
    slug = 'incoming'
    table_classes = (tables.IncomingTable,)
    template_name = 'horizon/common/_detail_table.html'
    preload = True

    def get_incoming_data(self):
        return _list(self.request, 'incoming', tables.SHOWN_LIVE)


class HistoryTab(tabs.TableTab):
    name = _("History")
    slug = 'history'
    table_classes = (tables.HistoryTable,)
    template_name = 'horizon/common/_detail_table.html'
    preload = True

    def get_history_data(self):
        return _list(self.request, None, tables.FINISHED, newest=True)


class PeeringTabs(tabs.TabGroup):
    slug = 'peering_tabs'
    tabs = (OutgoingTab, IncomingTab, HistoryTab)
    sticky = True
