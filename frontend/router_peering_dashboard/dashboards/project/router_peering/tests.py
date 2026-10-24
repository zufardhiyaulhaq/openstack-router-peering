from django.urls import reverse

from router_peering_dashboard.test import helpers as test

INDEX_URL = reverse('horizon:project:router_peering:index')
INDEX_TEMPLATE = 'project/router_peering/index.html'


class PanelTests(test.TestCase):
    def test_index_renders(self):
        res = self.client.get(INDEX_URL)
        self.assertEqual(res.status_code, 200)
        self.assertTemplateUsed(res, INDEX_TEMPLATE)
        self.assertContains(res, 'Router Peering')

    def test_panel_hidden_without_service(self):
        catalog = [s for s in self.service_catalog if s['type'] != 'router-peering']
        self.setActiveUser(id=self.user.id, token=self.token, username=self.user.name,
                           tenant_id=self.tenant.id, service_catalog=catalog,
                           authorized_tenants=self.tenants.list())
        res = self.client.get(INDEX_URL)
        self.assertIn(res.status_code, (302, 403))
