-- 0001_init: schema from design spec §6.1.

CREATE TYPE peering_status AS ENUM (
  'pending_acceptance','provisioning','active','degraded',
  'rejected','cancelled','expired','failed','deleting','deleted');

CREATE TABLE peerings (
  id                    text PRIMARY KEY,              -- 'rpx-' || 12 hex; regenerate on clash
  name                  text NOT NULL,
  status                peering_status NOT NULL DEFAULT 'pending_acceptance',
  status_reason         text,

  requester_project_id  varchar(64) NOT NULL,          -- Keystone id: 32 hex, no dashes
  requester_router_id   uuid NOT NULL,
  accepter_project_id   varchar(64) NOT NULL,
  accepter_router_id    uuid,                          -- set on accept

  transit_cidr          cidr,                          -- set by reconciler
  transit_network_id    uuid,
  transit_subnet_id     uuid,
  requester_port_id     uuid,
  accepter_port_id      uuid,
  requester_ip          inet,                          -- accepter's next hop
  accepter_ip           inet,                          -- requester's next hop

  attempts              int NOT NULL DEFAULT 0,
  next_attempt_at       timestamptz NOT NULL DEFAULT now(),
  claimed_by            text,                          -- reconciler instance id
  claimed_until         timestamptz,                   -- lease end; NULL or past = free
  warnings              jsonb NOT NULL DEFAULT '[]',   -- overlap found after accept (§9)

  created_by            varchar(64) NOT NULL,          -- Keystone user ids
  accepted_by           varchar(64),
  deleted_by            varchar(64),
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  expires_at            timestamptz NOT NULL DEFAULT now() + interval '7 days',
  deleted_at            timestamptz,

  CHECK (requester_project_id <> accepter_project_id)
);

-- one live peering per router pair, either direction
CREATE UNIQUE INDEX peerings_router_pair ON peerings
  (LEAST(requester_router_id, accepter_router_id), GREATEST(requester_router_id, accepter_router_id))
  WHERE accepter_router_id IS NOT NULL
    AND status NOT IN ('rejected','cancelled','expired','failed','deleted');

CREATE INDEX ON peerings (requester_project_id, status);
CREATE INDEX ON peerings (accepter_project_id, status);
CREATE INDEX ON peerings (next_attempt_at) WHERE status IN ('provisioning','deleting','active','degraded');

CREATE TABLE transit_blocks (
  cidr        cidr PRIMARY KEY,                        -- one /29
  peering_id  text UNIQUE REFERENCES peerings(id),     -- NULL = free
  reserved    boolean NOT NULL DEFAULT false           -- overlaps an existing subnet (§6.3)
);

CREATE TABLE peering_events (
  id                bigserial PRIMARY KEY,
  peering_id        text NOT NULL REFERENCES peerings(id),
  at                timestamptz NOT NULL DEFAULT now(),
  actor_user_id     varchar(64),                       -- NULL = reconciler
  actor_project_id  varchar(64),
  action            text NOT NULL,
  detail            jsonb
);
