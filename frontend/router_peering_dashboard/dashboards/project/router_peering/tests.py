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


def by_direction(outgoing=(), incoming=(), history=()):
    """Fake api.list that honours direction and the status filter the tabs pass."""
    def fake(request, direction=None, statuses=None, newest=False):
        rows = {'outgoing': outgoing, 'incoming': incoming}.get(direction, history)
        return [p for p in rows if statuses is None or p['status'] in statuses]
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
        self.assertContains(res, 'Peer Router')
        self.assertEqual({c.kwargs.get('direction') for c in list_.call_args_list}, {'incoming', 'outgoing', None})

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


from openstack_dashboard.api import neutron as neutron_api  # noqa: E402

CREATE_URL = reverse('horizon:project:router_peering:create')


class CreateTests(test.TestCase):
    @mock.patch.object(api, 'list', return_value=[])
    def test_create_button_on_outgoing(self, list_):
        self.assertContains(self.client.get(INDEX_URL), 'outgoing__action_create')

    @mock.patch.object(neutron_api, 'router_list')
    def test_create_get(self, router_list):
        router_list.return_value = self.routers.list()
        res = self.client.get(CREATE_URL)
        self.assertTemplateUsed(res, 'project/router_peering/create.html')
        self.assertContains(res, self.routers.first().name)
        self.assertContains(res, 'openstack token issue -c project_id')
        self.assertContains(res, 'Peer router ID')
        router_list.assert_called_once_with(test.IsHttpRequest(), tenant_id=self.request.user.tenant_id)

    @mock.patch.object(api, 'create')
    @mock.patch.object(neutron_api, 'router_list')
    def test_create_post_ok(self, router_list, create):
        router_list.return_value = self.routers.list()
        router = self.routers.first()
        res = self.client.post(CREATE_URL, {'name': 'a-to-b', 'router_id': router.id,
                                            'peer_project_id': 'b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0',
                                            'peer_router_id': 'bbbbbbbb-0000-0000-0000-000000000002'})
        self.assertNoFormErrors(res)
        self.assertRedirectsNoFollow(res, INDEX_URL)
        create.assert_called_once_with(test.IsHttpRequest(), 'a-to-b', router.id, 'b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0', 'bbbbbbbb-0000-0000-0000-000000000002')

    @mock.patch.object(api, 'create')
    @mock.patch.object(neutron_api, 'router_list')
    def test_create_api_error(self, router_list, create):
        router_list.return_value = self.routers.list()
        create.side_effect = api.APIError(409, 'project has reached its limit of open peerings')
        res = self.client.post(CREATE_URL, {'name': 'x', 'router_id': self.routers.first().id, 'peer_project_id': 'pb',
                                            'peer_router_id': 'bbbbbbbb-0000-0000-0000-000000000002'})
        self.assertEqual(res.status_code, 200)
        self.assertContains(res, 'project has reached its limit of open peerings')

    @mock.patch.object(neutron_api, 'router_list', return_value=[])
    def test_create_no_routers(self, router_list):
        self.assertContains(self.client.get(CREATE_URL), 'No routers in this project')


ACCEPT_URL = reverse('horizon:project:router_peering:accept', args=[PID])
REQUESTER = 'a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0'
MY_ROUTER = 'cccccccc-0000-0000-0000-000000000003'


def incoming_pending(**over):
    p = test.peering(role='accepter', peer_project_id=REQUESTER, peer_project_name='team-a', router_id=MY_ROUTER,
                     peer_router_id='aaaaaaaa-0000-0000-0000-000000000001')
    p.update(over)
    return p


class AcceptTests(test.TestCase):
    @mock.patch.object(api, 'list')
    def test_accept_link_by_status(self, list_):
        for table, p, want in [('incoming', incoming_pending(), True),
                               ('outgoing', test.peering(), False),
                               ('incoming', incoming_pending(status='active'), False)]:
            list_.side_effect = by_direction([p] if table == 'outgoing' else [], [p] if table == 'incoming' else [])
            html = self.client.get(INDEX_URL).content.decode()
            self.assertEqual(ACCEPT_URL in html, want, '%s %s' % (table, p['status']))

    @mock.patch.object(neutron_api, 'router_get')
    @mock.patch.object(api, 'get')
    def test_accept_get_shows_requester_and_my_router(self, get, router_get):
        get.return_value = incoming_pending()
        router_get.return_value = mock.Mock(id=MY_ROUTER, name='my-router')
        router_get.return_value.name = 'my-router'
        res = self.client.get(ACCEPT_URL)
        self.assertTemplateUsed(res, 'project/router_peering/accept.html')
        for text in ('team-a', REQUESTER, 'my-router (%s)' % MY_ROUTER):
            self.assertContains(res, text)
        self.assertNotContains(res, 'name="router_id"')  # confirm only: no router choice
        router_get.assert_called_once_with(test.IsHttpRequest(), MY_ROUTER)

    @mock.patch.object(neutron_api, 'router_get', side_effect=Exception('neutron down'))
    @mock.patch.object(api, 'get')
    def test_accept_get_router_name_unavailable(self, get, router_get):
        get.return_value = incoming_pending()
        self.assertContains(self.client.get(ACCEPT_URL), MY_ROUTER)

    @mock.patch.object(api, 'accept')
    @mock.patch.object(neutron_api, 'router_get')
    @mock.patch.object(api, 'get')
    def test_accept_post_ok(self, get, router_get, accept):
        get.return_value = incoming_pending()
        res = self.client.post(ACCEPT_URL, {'peering_id': PID})
        self.assertNoFormErrors(res)
        self.assertRedirectsNoFollow(res, INDEX_URL)
        accept.assert_called_once_with(test.IsHttpRequest(), PID)

    @mock.patch.object(api, 'accept')
    @mock.patch.object(neutron_api, 'router_get')
    @mock.patch.object(api, 'get')
    def test_accept_overlap_message(self, get, router_get, accept):
        get.return_value = incoming_pending()
        accept.side_effect = api.APIError(409, 'CIDRs overlap between the 2 sides', ['10.1.0.0/24', '10.2.0.0/24'])
        res = self.client.post(ACCEPT_URL, {'peering_id': PID})
        self.assertEqual(res.status_code, 200)
        self.assertContains(res, 'CIDRs overlap between the 2 sides: 10.1.0.0/24, 10.2.0.0/24')

    @mock.patch.object(api, 'get')
    def test_accept_unknown_peering_redirects(self, get):
        get.side_effect = api.APIError(404, 'peering not found')
        res = self.client.get(ACCEPT_URL)
        self.assertRedirectsNoFollow(res, INDEX_URL)


DETAIL_URL = reverse('horizon:project:router_peering:detail', args=[PID])
HINT = 'openstack router set --route destination=<peer subnet>,gateway=100.64.0.2 aaaaaaaa-0000-0000-0000-000000000001'


class DetailTests(test.TestCase):
    @mock.patch.object(api, 'list')
    def test_name_links_to_detail(self, list_):
        list_.side_effect = by_direction([test.peering()])
        self.assertContains(self.client.get(INDEX_URL), DETAIL_URL)

    @mock.patch.object(api, 'events', return_value=[])
    @mock.patch.object(api, 'get')
    def test_detail_active_shows_hints(self, get, events):
        get.return_value = test.peering(status='active', next_hop='100.64.0.2',
                                        route_hints=[HINT], warnings=['10.9.0.0/24 overlaps with the peer'])
        res = self.client.get(DETAIL_URL)
        self.assertTemplateUsed(res, 'project/router_peering/detail.html')
        for text in (HINT.replace('<', '&lt;').replace('>', '&gt;'), '100.64.0.2', '10.9.0.0/24 overlaps with the peer',
                     'Peer router ID', 'bbbbbbbb-0000-0000-0000-000000000001'):
            self.assertContains(res, text)
        self.assertNotContains(res, 'Peer CIDRs')

    @mock.patch.object(api, 'events', return_value=[])
    @mock.patch.object(api, 'get')
    def test_detail_pending_no_hints(self, get, events):
        get.return_value = test.peering()
        res = self.client.get(DETAIL_URL)
        self.assertContains(res, 'Route hints appear once the peering is active')
        self.assertNotContains(res, '<pre')

    @mock.patch.object(api, 'events')
    @mock.patch.object(api, 'get')
    def test_detail_events(self, get, events):
        get.return_value = test.peering(status='rejected')
        events.return_value = [
            {'at': '2026-10-24T08:00:00Z', 'action': 'requested', 'actor_user_id': 'user-a', 'actor_project_id': 'pa'},
            {'at': '2026-10-24T09:00:00Z', 'action': 'rejected'},
        ]
        res = self.client.get(DETAIL_URL)
        self.assertContains(res, 'requested')
        self.assertContains(res, 'user-a')
        self.assertContains(res, 'rejected')

    @mock.patch.object(api, 'get')
    def test_detail_not_found_redirects(self, get):
        get.side_effect = api.APIError(404, 'peering not found')
        self.assertRedirectsNoFollow(self.client.get(DETAIL_URL), INDEX_URL)

    @mock.patch.object(api, 'events')
    @mock.patch.object(api, 'get')
    def test_detail_events_api_error(self, get, events):
        get.return_value = test.peering()
        events.side_effect = api.APIError(503, 'router peering service unreachable')
        res = self.client.get(DETAIL_URL)
        self.assertEqual(res.status_code, 200)
        self.assertContains(res, 'router peering service unreachable')


LIVE = ['pending_acceptance', 'provisioning', 'active', 'degraded', 'deleting']
FINISHED = ['rejected', 'cancelled', 'expired', 'failed', 'deleted']


class HistoryTests(test.TestCase):
    @mock.patch.object(api, 'list')
    def test_tabs_split_live_and_history(self, list_):
        live = test.peering(name='live-one', status='active')
        done = test.peering(id='rpx-ffffffffffff', name='done-one', status='rejected')
        list_.side_effect = by_direction([live, done], [], [done])
        html = self.client.get(INDEX_URL).content.decode()
        calls = {c.kwargs.get('direction'): (c.kwargs.get('statuses'), c.kwargs.get('newest', False)) for c in list_.call_args_list}
        # History asks for the newest first, so a long history never hides the latest finished peerings.
        self.assertEqual(calls, {'outgoing': (LIVE, False), 'incoming': (LIVE, False), None: (FINISHED, True)})
        self.assertIn('live-one', html)
        self.assertIn('done-one', html)
        self.assertNotIn('outgoing__row_rpx-ffffffffffff', html)  # a finished peering never shows in a live tab
        self.assertIn('History', html)

    @mock.patch.object(api, 'list')
    def test_history_has_no_actions(self, list_):
        done = test.peering(id='rpx-ffffffffffff', name='done-one', status='deleted')
        list_.side_effect = by_direction([], [], [done])
        html = self.client.get(INDEX_URL).content.decode()
        self.assertIn('done-one', html)
        self.assertNotIn('history__', html.split('done-one')[1].split('</tr>')[0].replace('history__row', ''))
