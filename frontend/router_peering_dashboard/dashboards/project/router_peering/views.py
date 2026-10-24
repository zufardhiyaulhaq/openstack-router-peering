from django.utils.translation import gettext_lazy as _
from horizon import tabs

from router_peering_dashboard.dashboards.project.router_peering import tabs as rp_tabs


class IndexView(tabs.TabbedTableView):
    tab_group_class = rp_tabs.PeeringTabs
    template_name = 'project/router_peering/index.html'
    page_title = _("Router Peering")
