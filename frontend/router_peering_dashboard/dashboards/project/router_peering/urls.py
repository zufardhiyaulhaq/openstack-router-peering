from django.urls import re_path

from router_peering_dashboard.dashboards.project.router_peering import views

urlpatterns = [
    re_path(r'^$', views.IndexView.as_view(), name='index'),
    re_path(r'^create/$', views.CreateView.as_view(), name='create'),
    re_path(r'^(?P<peering_id>[^/]+)/accept/$', views.AcceptView.as_view(), name='accept'),
]
