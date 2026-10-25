from unittest import mock

import requests

from router_peering_dashboard.api import peering
from router_peering_dashboard.test import helpers as test

BASE = test.ROUTER_PEERING_URL


def response(status, body):
    r = mock.Mock(status_code=status)
    r.ok = 200 <= status < 300
    r.json.return_value = body
    r.content = b'x' if body is not None else b''
    return r


class APITests(test.TestCase):
    def setUp(self):
        super().setUp()
        self.request = test.api_request(self.service_catalog)

    def call(self, fn, *args, status=200, body=None, **kwargs):
        with mock.patch.object(peering.requests, 'request', return_value=response(status, body)) as req:
            out = fn(self.request, *args, **kwargs)
        return out, req

    def assertSent(self, req, method, path, json=None):
        args, kwargs = req.call_args
        self.assertEqual((args[0], args[1]), (method, BASE + path))
        self.assertEqual(kwargs['headers']['X-Auth-Token'], self.request.user.token.id)
        self.assertEqual(kwargs.get('json'), json)
        self.assertEqual(kwargs['timeout'], 10)
        self.assertIs(kwargs['allow_redirects'], False)  # never send the user's token to a redirect target

    def test_list(self):
        out, req = self.call(peering.list, direction='incoming', body=[test.peering()])
        self.assertSent(req, 'GET', '/v1/peerings?direction=incoming&limit=100')
        self.assertEqual(out[0]['id'], 'rpx-0123456789ab')
        _, req = self.call(peering.list, body=[])
        self.assertSent(req, 'GET', '/v1/peerings?limit=100')

    def test_get_and_events(self):
        _, req = self.call(peering.get, 'rpx-1', body=test.peering())
        self.assertSent(req, 'GET', '/v1/peerings/rpx-1')
        _, req = self.call(peering.events, 'rpx-1', body=[])
        self.assertSent(req, 'GET', '/v1/peerings/rpx-1/events')

    def test_writes(self):
        _, req = self.call(peering.create, 'n', 'r1', 'pb', 'r2', status=201, body=test.peering())
        self.assertSent(req, 'POST', '/v1/peerings', {'name': 'n', 'router_id': 'r1', 'peer_project_id': 'pb', 'peer_router_id': 'r2'})
        _, req = self.call(peering.accept, 'rpx-1', status=202, body=test.peering())
        self.assertSent(req, 'POST', '/v1/peerings/rpx-1/accept')
        _, req = self.call(peering.reject, 'rpx-1', body=test.peering())
        self.assertSent(req, 'POST', '/v1/peerings/rpx-1/reject')
        _, req = self.call(peering.repair, 'rpx-1', status=202, body=test.peering())
        self.assertSent(req, 'POST', '/v1/peerings/rpx-1/repair')
        _, req = self.call(peering.delete, 'rpx-1', status=202, body=test.peering())
        self.assertSent(req, 'DELETE', '/v1/peerings/rpx-1')

    def test_error_with_overlaps(self):
        with self.assertRaises(peering.APIError) as cm:
            self.call(peering.accept, 'rpx-1', status=409,
                      body={'error': 'CIDRs overlap', 'overlaps': ['10.0.0.0/24']})
        self.assertEqual((cm.exception.status, cm.exception.message, cm.exception.overlaps),
                         (409, 'CIDRs overlap', ['10.0.0.0/24']))

    def test_error_without_json(self):
        r = response(502, None)
        r.json.side_effect = ValueError('not json')
        with mock.patch.object(peering.requests, 'request', return_value=r):
            with self.assertRaises(peering.APIError) as cm:
                peering.get(self.request, 'rpx-1')
        self.assertEqual(cm.exception.status, 502)

    def test_unreachable(self):
        with mock.patch.object(peering.requests, 'request', side_effect=requests.ConnectionError('boom')):
            with self.assertRaises(peering.APIError) as cm:
                peering.list(self.request)
        self.assertEqual((cm.exception.status, cm.exception.message), (503, 'router peering service unreachable'))

    def test_list_follows_pages(self):
        page1 = [test.peering(id='rpx-%012d' % i) for i in range(100)]
        page2 = [test.peering(id='rpx-new-pending')]
        with mock.patch.object(peering.requests, 'request',
                               side_effect=[response(200, page1), response(200, page2)]) as req:
            out = peering.list(self.request, direction='incoming')
        self.assertEqual(len(out), 101)
        self.assertEqual(out[-1]['id'], 'rpx-new-pending')
        self.assertEqual(req.call_args_list[1].args[1],
                         BASE + '/v1/peerings?direction=incoming&limit=100&marker=rpx-000000000099')

    def test_list_page_cap(self):
        full = [test.peering(id='rpx-x') for _ in range(100)]
        with mock.patch.object(peering.requests, 'request', return_value=response(200, full)) as req:
            out = peering.list(self.request)
        self.assertEqual(req.call_count, peering.MAX_PAGES)
        self.assertEqual(len(out), 100 * peering.MAX_PAGES)

    def test_service_missing_from_catalog(self):
        from horizon import exceptions as horizon_exceptions
        with mock.patch.object(peering.base, 'url_for', side_effect=horizon_exceptions.ServiceCatalogException('router-peering')):
            with self.assertRaises(peering.APIError) as cm:
                peering.list(self.request)
        self.assertEqual(cm.exception.status, 503)

    def test_list_status_filter_survives_paging(self):
        page1 = [test.peering(id='rpx-%012d' % i) for i in range(100)]
        with mock.patch.object(peering.requests, 'request',
                               side_effect=[response(200, page1), response(200, [])]) as req:
            peering.list(self.request, direction='outgoing', statuses=['pending_acceptance', 'active'])
        for call in req.call_args_list:
            url = call.args[1]
            self.assertIn('status=pending_acceptance&status=active', url)
            self.assertIn('direction=outgoing', url)
        self.assertIn('marker=rpx-000000000099', req.call_args_list[1].args[1])

    def test_list_newest_first(self):
        with mock.patch.object(peering.requests, 'request', return_value=response(200, [])) as req:
            peering.list(self.request, statuses=['deleted'], newest=True)
        self.assertIn('sort=newest', req.call_args.args[1])
        with mock.patch.object(peering.requests, 'request', return_value=response(200, [])) as req:
            peering.list(self.request)
        self.assertNotIn('sort=', req.call_args.args[1])
