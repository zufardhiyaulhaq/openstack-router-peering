from django import http
from openstack_dashboard.test import helpers

ROUTER_PEERING_URL = 'http://router-peering.example.com:9797'


class TestCase(helpers.TestCase):
    """Horizon's TestCase with router-peering in the user's service catalog."""

    def _setup_test_data(self):
        super()._setup_test_data()
        self.service_catalog.append({
            'type': 'router-peering', 'name': 'router-peering', 'endpoints_links': [],
            'endpoints': [{'region': 'RegionOne', 'interface': i, 'url': ROUTER_PEERING_URL}
                          for i in ('admin', 'internal', 'public')],
        })


def peering(**over):
    """An API-shaped peering view (design spec §4.2), as the requester sees it unless overridden."""
    p = {
        'id': 'rpx-0123456789ab', 'name': 'a-to-b', 'status': 'pending_acceptance', 'status_reason': '',
        'role': 'requester', 'router_id': 'aaaaaaaa-0000-0000-0000-000000000001',
        'peer_project_id': 'b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0', 'peer_project_name': '',
        'next_hop': '', 'peer_cidrs': [], 'route_hints': [], 'warnings': [],
        'created_at': '2026-10-24T08:00:00Z', 'expires_at': '2026-10-31T08:00:00Z',
    }
    p.update(over)
    return p


class IsHttpRequest:
    def __eq__(self, other):
        return isinstance(other, http.HttpRequest)
