# Test settings: Horizon's own test settings plus this plugin, the same way upstream plugins do it.
from horizon.test.settings import *  # noqa: F403,H303
from openstack_dashboard.test.settings import *  # noqa: F403,H303

import openstack_dashboard.enabled
from openstack_dashboard.utils import settings

import router_peering_dashboard.enabled

HORIZON_CONFIG.pop('dashboards', None)  # noqa: F405
HORIZON_CONFIG.pop('default_dashboard', None)  # noqa: F405
settings.update_dashboards(
    [router_peering_dashboard.enabled, openstack_dashboard.enabled],
    HORIZON_CONFIG,  # noqa: F405
    INSTALLED_APPS,  # noqa: F405
)
INSTALLED_APPS = list(set(INSTALLED_APPS))  # noqa: F405
