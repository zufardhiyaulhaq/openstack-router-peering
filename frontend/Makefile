# Tests run inside the Kolla Horizon image the plugin is deployed on, so versions match production.
IMAGE ?= quay.io/openstack.kolla/horizon:2026.1-ubuntu-noble

.PHONY: test
test:
	docker run --rm --platform linux/amd64 --user 0 -e PYTHONDONTWRITEBYTECODE=1 \
		-v $(CURDIR):/src -w /src --entrypoint /var/lib/kolla/venv/bin/python $(IMAGE) \
		-m django test router_peering_dashboard --settings=router_peering_dashboard.test.settings $(ARGS)
