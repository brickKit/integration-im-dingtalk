-- Outbox / Inbox。所有组件都有这两张表，且都按 created_at 周分区
-- （§11.2.5）。本组件消费 infra.notification.dispatch.im.v1、发布
-- integration.im.result.v1。
--
-- ⚠️ 初始分区覆盖当前周起 4 周（迁移执行时是 2026-09-07 那一周）。
-- 其余分区由组件内置定时任务自动建（决策 54、§11.5.1）。

CREATE TABLE event_outbox (
    id           BIGSERIAL,
    subject      TEXT        NOT NULL,
    aggregate_id TEXT        NOT NULL,
    version      BIGINT      NOT NULL,
    trace_id     TEXT        NOT NULL DEFAULT '',
    causation_id TEXT        NOT NULL DEFAULT '',
    hop_count    INT         NOT NULL DEFAULT 0,
    payload      JSONB       NOT NULL,
    published_at TIMESTAMPTZ,
    attempts     INT         NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    status       TEXT        NOT NULL DEFAULT 'PENDING',
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE TABLE event_outbox_2026_09_07 PARTITION OF event_outbox
  FOR VALUES FROM ('2026-09-07') TO ('2026-09-14');
CREATE TABLE event_outbox_2026_09_14 PARTITION OF event_outbox
  FOR VALUES FROM ('2026-09-14') TO ('2026-09-21');
CREATE TABLE event_outbox_2026_09_21 PARTITION OF event_outbox
  FOR VALUES FROM ('2026-09-21') TO ('2026-09-28');
CREATE TABLE event_outbox_2026_09_28 PARTITION OF event_outbox
  FOR VALUES FROM ('2026-09-28') TO ('2026-10-05');
CREATE INDEX event_outbox_pending ON event_outbox (status, created_at)
  WHERE status = 'PENDING';
ALTER TABLE event_outbox OWNER TO integration_im_dingtalk_rw;

CREATE TABLE event_inbox (
    id              BIGSERIAL,
    idempotency_key TEXT        NOT NULL,
    subject         TEXT        NOT NULL,
    aggregate_id    TEXT        NOT NULL,
    version         BIGINT      NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    status          TEXT        NOT NULL DEFAULT 'PROCESSED',
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE TABLE event_inbox_2026_09_07 PARTITION OF event_inbox
  FOR VALUES FROM ('2026-09-07') TO ('2026-09-14');
CREATE TABLE event_inbox_2026_09_14 PARTITION OF event_inbox
  FOR VALUES FROM ('2026-09-14') TO ('2026-09-21');
CREATE TABLE event_inbox_2026_09_21 PARTITION OF event_inbox
  FOR VALUES FROM ('2026-09-21') TO ('2026-09-28');
CREATE TABLE event_inbox_2026_09_28 PARTITION OF event_inbox
  FOR VALUES FROM ('2026-09-28') TO ('2026-10-05');
CREATE UNIQUE INDEX event_inbox_idem ON event_inbox (idempotency_key, created_at);
ALTER TABLE event_inbox OWNER TO integration_im_dingtalk_rw;

-- ⚠️ 本组件刻意没有 command_idempotency 表——这是设计计划初版列出过、
-- 核对既有先例后去掉的一张表（同 infra-workflow CreateTask 幂等设计
-- 那次自我纠正是同一类判断）：全项目搜过，command_idempotency 只用于
-- "外部调用方提供 idempotency_key 的写命令 RPC"（如 erp-inventory 的
-- Reserve、infra-workflow 的 CreateTask），从来不用于事件消费路径——
-- 事件消费的去重完全由上面的 event_inbox（按 subject+aggregate_id+
-- version）负责。本组件没有任何对外暴露的写 rpc（§3 明确没有
-- SendMessage 这类接口），唯一的写入动作是消费 dispatch.im.v1，
-- event_inbox 已经是这条路径唯一需要、也是唯一被使用过的幂等机制，
-- 再叠一张 command_idempotency 是没有对应真实场景的多余抽象。
