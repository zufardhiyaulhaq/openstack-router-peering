from django.urls import reverse
from django.urls import reverse_lazy
from django.utils.translation import gettext_lazy as _
from horizon import exceptions
from horizon import forms
from horizon import tabs

from router_peering_dashboard.api import peering as api

from router_peering_dashboard.dashboards.project.router_peering import forms as rp_forms
from router_peering_dashboard.dashboards.project.router_peering import tabs as rp_tabs


class IndexView(tabs.TabbedTableView):
    tab_group_class = rp_tabs.PeeringTabs
    template_name = 'project/router_peering/index.html'
    page_title = _("Router Peering")


class CreateView(forms.ModalFormView):
    form_class = rp_forms.CreatePeeringForm
    template_name = 'project/router_peering/create.html'
    modal_id = 'create_router_peering_modal'
    page_title = _("Request Peering")
    submit_label = _("Request Peering")
    submit_url = reverse_lazy('horizon:project:router_peering:create')
    success_url = reverse_lazy('horizon:project:router_peering:index')


def get_peering_or_redirect(request, peering_id):
    """The peering as this user sees it; on any API error, back to the index with the message."""
    try:
        return api.get(request, peering_id)
    except api.APIError as e:
        raise exceptions.Http302(reverse('horizon:project:router_peering:index'), message=e.message)


class AcceptView(forms.ModalFormView):
    form_class = rp_forms.AcceptPeeringForm
    template_name = 'project/router_peering/accept.html'
    modal_id = 'accept_router_peering_modal'
    page_title = _("Accept Peering")
    submit_label = _("Accept")
    success_url = reverse_lazy('horizon:project:router_peering:index')

    def get_initial(self):
        p = get_peering_or_redirect(self.request, self.kwargs['peering_id'])
        name = p.get('peer_project_name') or ''
        label = '%s (%s)' % (name, p['peer_project_id']) if name else p['peer_project_id']
        return {'peering_id': p['id'], 'requester': label}

    def get_context_data(self, **kwargs):
        context = super().get_context_data(**kwargs)
        context['submit_url'] = reverse('horizon:project:router_peering:accept', args=[self.kwargs['peering_id']])
        return context
