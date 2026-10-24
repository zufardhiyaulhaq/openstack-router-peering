"""Client for the router-peering API, acting as the logged-in Horizon user."""
from django.conf import settings
from openstack_dashboard.api import base
import requests

SERVICE_TYPE = 'router-peering'
TIMEOUT = 10


class APIError(Exception):
    """A non-2xx answer (or no answer) from the router-peering API."""

    def __init__(self, status, message, overlaps=None):
        super().__init__(message)
        self.status = status
        self.message = message
        self.overlaps = overlaps or []


def _verify():
    if getattr(settings, 'OPENSTACK_SSL_NO_VERIFY', False):
        return False
    return getattr(settings, 'OPENSTACK_SSL_CACERT', None) or True


def _call(request, method, path, json=None):
    url = base.url_for(request, SERVICE_TYPE).rstrip('/') + path
    try:
        resp = requests.request(method, url, json=json, timeout=TIMEOUT, verify=_verify(),
                                headers={'X-Auth-Token': request.user.token.id})
    except requests.RequestException:
        raise APIError(503, 'router peering service unreachable')
    try:
        body = resp.json() if resp.content else None
    except ValueError:
        body = None
    if not resp.ok:
        body = body if isinstance(body, dict) else {}
        raise APIError(resp.status_code, body.get('error') or 'router peering request failed (HTTP %s)' % resp.status_code,
                       body.get('overlaps'))
    return body


def list(request, direction=None):  # noqa: A001 - mirrors the API verb
    path = '/v1/peerings' + ('?direction=%s' % direction if direction else '')
    return _call(request, 'GET', path) or []


def get(request, peering_id):
    return _call(request, 'GET', '/v1/peerings/%s' % peering_id)


def events(request, peering_id):
    return _call(request, 'GET', '/v1/peerings/%s/events' % peering_id) or []


def create(request, name, router_id, peer_project_id):
    return _call(request, 'POST', '/v1/peerings',
                 {'name': name, 'router_id': router_id, 'peer_project_id': peer_project_id})


def accept(request, peering_id, router_id):
    return _call(request, 'POST', '/v1/peerings/%s/accept' % peering_id, {'router_id': router_id})


def reject(request, peering_id):
    return _call(request, 'POST', '/v1/peerings/%s/reject' % peering_id)


def repair(request, peering_id):
    return _call(request, 'POST', '/v1/peerings/%s/repair' % peering_id)


def delete(request, peering_id):
    return _call(request, 'DELETE', '/v1/peerings/%s' % peering_id)
