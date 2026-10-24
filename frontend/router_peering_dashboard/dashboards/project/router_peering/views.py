from django.utils.translation import gettext_lazy as _
from horizon import views


class IndexView(views.HorizonTemplateView):
    template_name = 'project/router_peering/index.html'
    page_title = _("Router Peering")
