from django.contrib import messages
from django.utils.translation import gettext_lazy as _
from horizon import forms
from openstack_dashboard.api import neutron

from router_peering_dashboard.api import peering as api


def router_choices(request):
    """The caller's routers as (id, label) choices, or a single disabled-looking hint when there are none."""
    try:
        routers = neutron.router_list(request, tenant_id=request.user.tenant_id)
    except Exception:
        messages.error(request, _("Unable to list your routers."))
        routers = []
    if not routers:
        return [('', _("No routers in this project"))]
    return [('', _("Select a router"))] + [(r.id, '%s (%s)' % (r.name or r.id, r.id[:8])) for r in routers]


class CreatePeeringForm(forms.SelfHandlingForm):
    name = forms.CharField(label=_("Name"), max_length=255)
    router_id = forms.ChoiceField(label=_("Your router"))
    peer_project_id = forms.CharField(
        label=_("Peer project ID"), max_length=64,
        help_text=_("Ask the other project for its ID. They can find it with "
                    "'openstack token issue -c project_id' or under API Access."))

    def __init__(self, request, *args, **kwargs):
        super().__init__(request, *args, **kwargs)
        self.fields['router_id'].choices = router_choices(request)

    def handle(self, request, data):
        try:
            api.create(request, data['name'], data['router_id'], data['peer_project_id'].strip())
        except api.APIError as e:
            self.api_error(e.message)
            return False
        messages.success(request, _("Peering requested. The other project must accept it."))
        return True


class AcceptPeeringForm(forms.SelfHandlingForm):
    peering_id = forms.CharField(widget=forms.HiddenInput())
    requester = forms.CharField(label=_("Requested by project"), required=False,
                                widget=forms.TextInput(attrs={'readonly': 'readonly'}))
    router_id = forms.ChoiceField(label=_("Your router"),
                                  help_text=_("The router in this project to connect. Its subnets must not "
                                              "overlap the requester's."))

    def __init__(self, request, *args, **kwargs):
        super().__init__(request, *args, **kwargs)
        self.fields['router_id'].choices = router_choices(request)

    def handle(self, request, data):
        try:
            api.accept(request, data['peering_id'], data['router_id'])
        except api.APIError as e:
            msg = e.message
            if e.overlaps:
                msg = '%s: %s' % (msg, ', '.join(e.overlaps))
            self.api_error(msg)
            return False
        messages.success(request, _("Peering accepted. Building the transit network…"))
        return True
