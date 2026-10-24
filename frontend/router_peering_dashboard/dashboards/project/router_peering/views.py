from django.urls import reverse_lazy
from django.utils.translation import gettext_lazy as _
from horizon import forms
from horizon import tabs

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
