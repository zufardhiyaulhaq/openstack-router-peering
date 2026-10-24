from unittest import mock

from django.urls import reverse

from router_peering_dashboard.test import helpers as test

INDEX_URL = reverse('horizon:project:router_peering:index')
INDEX_TEMPLATE = 'project/router_peering/index.html'


class PanelTests(test.TestCase):
    @mock.patch('router_peering_dashboard.api.peering.list', return_value=[])
    def test_index_renders(self, list_):
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


from router_peering_dashboard.api import peering as api  # noqa: E402

PID = 'rpx-0123456789ab'


def by_direction(outgoing=(), incoming=()):
    def fake(request, direction=None):
        return list(outgoing) if direction == 'outgoing' else list(incoming)
    return fake


class IndexTests(test.TestCase):
    @mock.patch.object(api, 'list')
    def test_index_lists_both_tabs(self, list_):
        list_.side_effect = by_direction([test.peering(name='out-one')],
                                         [test.peering(id='rpx-bbbbbbbbbbbb', name='in-one', role='accepter')])
        res = self.client.get(INDEX_URL)
        self.assertEqual(res.status_code, 200)
        self.assertContains(res, 'out-one')
        self.assertContains(res, 'in-one')
        self.assertEqual(sorted(c.kwargs.get('direction') for c in list_.call_args_list), ['incoming', 'outgoing'])

    @mock.patch.object(api, 'list')
    def test_index_api_error(self, list_):
        list_.side_effect = api.APIError(503, 'router peering service unreachable')
        res = self.client.get(INDEX_URL)
        self.assertEqual(res.status_code, 200)
        self.assertContains(res, 'router peering service unreachable')
        self.assertContains(res, 'No items to display')

    @mock.patch.object(api, 'list')
    def test_row_actions_by_status(self, list_):
        # (table, role, status) -> actions that must be offered; every other action must be absent.
        cases = [
            ('outgoing', 'requester', 'pending_acceptance', {'cancel'}),
            ('incoming', 'accepter', 'pending_acceptance', {'reject'}),
            ('outgoing', 'requester', 'provisioning', {'delete'}),
            ('incoming', 'accepter', 'active', {'delete'}),
            ('outgoing', 'requester', 'degraded', {'repair', 'delete'}),
            ('outgoing', 'requester', 'deleting', set()),
            ('incoming', 'accepter', 'rejected', set()),
        ]
        for table, role, status, want in cases:
            p = test.peering(role=role, status=status)
            list_.side_effect = by_direction([p] if table == 'outgoing' else [], [p] if table == 'incoming' else [])
            html = self.client.get(INDEX_URL).content.decode()
            for action in ('cancel', 'reject', 'repair', 'delete'):
                present = '%s__%s__%s' % (table, action, PID) in html
                self.assertEqual(present, action in want, '%s/%s/%s: action %s present=%s' % (table, role, status, action, present))

    @mock.patch.object(api, 'delete')
    @mock.patch.object(api, 'list')
    def test_delete_action_calls_api(self, list_, delete):
        list_.side_effect = by_direction([test.peering(status='active')])
        res = self.client.post(INDEX_URL, {'action': 'outgoing__delete__%s' % PID})
        self.assertRedirectsNoFollow(res, INDEX_URL)
        delete.assert_called_once_with(test.IsHttpRequest(), PID)

    @mock.patch.object(api, 'reject')
    @mock.patch.object(api, 'list')
    def test_reject_action_calls_api(self, list_, reject):
        list_.side_effect = by_direction([], [test.peering(role='accepter')])
        res = self.client.post(INDEX_URL, {'action': 'incoming__reject__%s' % PID})
        self.assertRedirectsNoFollow(res, INDEX_URL)
        reject.assert_called_once_with(test.IsHttpRequest(), PID)

    @mock.patch.object(api, 'delete')
    @mock.patch.object(api, 'list')
    def test_action_shows_api_error(self, list_, delete):
        list_.side_effect = by_direction([test.peering(status='active')])
        delete.side_effect = api.APIError(409, 'peering is already deleted')
        res = self.client.post(INDEX_URL, {'action': 'outgoing__delete__%s' % PID}, follow=True)
        self.assertContains(res, 'peering is already deleted')

    @mock.patch.object(api, 'list')
    def test_status_reason_shown(self, list_):
        list_.side_effect = by_direction([test.peering(status='deleting', status_reason='remove routes via 100.64.0.2 on router r1 first')])
        self.assertContains(self.client.get(INDEX_URL), 'remove routes via 100.64.0.2 on router r1 first')
