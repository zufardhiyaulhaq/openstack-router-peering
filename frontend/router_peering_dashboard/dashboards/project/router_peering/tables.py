from types import SimpleNamespace

from django.contrib import messages
from django.utils.translation import gettext_lazy as _
from django.utils.translation import ngettext_lazy
from horizon import tables

from router_peering_dashboard.api import peering as api

PENDING = 'pending_acceptance'
LIVE = ('provisioning', 'active', 'degraded')


def wrap(peerings):
    """API dicts -> attribute objects, which Horizon tables expect."""
    return [SimpleNamespace(**p) for p in peerings]


def _call(request, fn, peering_id):
    try:
        fn(request, peering_id)
    except api.APIError as e:
        messages.error(request, e.message)
        raise


class CreatePeering(tables.LinkAction):
    name = 'create'
    verbose_name = _("Request Peering")
    url = 'horizon:project:router_peering:create'
    classes = ('ajax-modal',)
    icon = 'plus'


class RejectPeering(tables.DeleteAction):
    name = 'reject'
    help_text = _("The requester is told the request was rejected. Nothing is built.")

    @staticmethod
    def action_present(count):
        return ngettext_lazy("Reject Request", "Reject Requests", count)

    @staticmethod
    def action_past(count):
        return ngettext_lazy("Rejected Request", "Rejected Requests", count)

    def allowed(self, request, datum=None):
        return datum is None or (datum.role == 'accepter' and datum.status == PENDING)

    def delete(self, request, obj_id):
        _call(request, api.reject, obj_id)


class CancelPeering(tables.DeleteAction):
    name = 'cancel'

    @staticmethod
    def action_present(count):
        return ngettext_lazy("Cancel Request", "Cancel Requests", count)

    @staticmethod
    def action_past(count):
        return ngettext_lazy("Cancelled Request", "Cancelled Requests", count)

    def allowed(self, request, datum=None):
        return datum is None or (datum.role == 'requester' and datum.status == PENDING)

    def delete(self, request, obj_id):
        _call(request, api.delete, obj_id)


class DeletePeering(tables.DeleteAction):
    name = 'delete'
    help_text = _("Removes the transit network from both routers. Remove your routes via the next hop first.")

    @staticmethod
    def action_present(count):
        return ngettext_lazy("Delete Peering", "Delete Peerings", count)

    @staticmethod
    def action_past(count):
        return ngettext_lazy("Deleted Peering", "Deleted Peerings", count)

    def allowed(self, request, datum=None):
        return datum is None or datum.status in LIVE

    def delete(self, request, obj_id):
        _call(request, api.delete, obj_id)


class RepairPeering(tables.BatchAction):
    name = 'repair'
    help_text = _("Re-creates a router interface that was removed, on the same next hop.")

    @staticmethod
    def action_present(count):
        return ngettext_lazy("Repair Peering", "Repair Peerings", count)

    @staticmethod
    def action_past(count):
        return ngettext_lazy("Repair requested for Peering", "Repair requested for Peerings", count)

    def allowed(self, request, datum=None):
        return datum is None or datum.status == 'degraded'

    def action(self, request, obj_id):
        _call(request, api.repair, obj_id)


def expires(p):
    return p.expires_at if p.status == PENDING else '-'


class PeeringsTable(tables.DataTable):
    name = tables.Column('name', verbose_name=_("Name"))
    peer_project_id = tables.Column('peer_project_id', verbose_name=_("Peer Project ID"))
    router_id = tables.Column('router_id', verbose_name=_("My Router"), empty_value='-')
    status = tables.Column('status', verbose_name=_("Status"))
    status_reason = tables.Column('status_reason', verbose_name=_("Details"), empty_value='-')
    next_hop = tables.Column('next_hop', verbose_name=_("Next Hop"), empty_value='-')
    expires = tables.Column(expires, verbose_name=_("Expires"))

    def get_object_id(self, datum):
        return datum.id

    def get_object_display(self, datum):
        return datum.name


class OutgoingTable(PeeringsTable):
    class Meta:
        name = 'outgoing'
        verbose_name = _("Outgoing")
        table_actions = (CreatePeering,)
        row_actions = (RepairPeering, CancelPeering, DeletePeering)


class IncomingTable(PeeringsTable):
    class Meta:
        name = 'incoming'
        verbose_name = _("Incoming")
        table_actions = ()
        row_actions = (RepairPeering, RejectPeering, DeletePeering)
