-- dingtalk_user_map：手机号 -> 钉钉 userid 的缓存（设计计划 §2）。
-- ⚠️ 钉钉那个查询接口有频控，不能每发一条查一次。
CREATE TABLE dingtalk_user_map (
    phone       TEXT        PRIMARY KEY,
    userid      TEXT        NOT NULL,
    resolved_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- ⚠️ 手机号是个人信息（设计计划 §2 ⚠️）：这张表只在本组件内部使用，
-- 不开任何对外查询接口，也不进任何事件 payload。

-- dingtalk_token：access_token 与过期时间，单行落库（设计计划 §2）——
-- 不只放内存是因为重启后重新申请会撞频控。
CREATE TABLE dingtalk_token (
    id           SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),  -- 单行约束
    access_token TEXT        NOT NULL DEFAULT '',
    expires_at   TIMESTAMPTZ,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO dingtalk_token (id) VALUES (1);

-- delivery_attempts：每次调用的结果，写入即终态，不改（设计计划 §2）——
-- ACCEPTED 与 CONFIRMED 是同一个 record_id 的两条独立记录（钉钉的
-- 异步两段式：asyncsend_v2 提交成功先记一条 ACCEPTED，随后 getsendresult
-- 查到真实结果再记一条 CONFIRMED，设计计划 §4.2），不是同一行原地更新。
-- BatchGetDeliveryStatus 按 record_id 取最新一条（phase 优先级
-- CONFIRMED > ACCEPTED，同 phase 取 attempted_at 最大的）。
CREATE TABLE delivery_attempts (
    id                BIGSERIAL,
    record_id         TEXT        NOT NULL,
    adapter           TEXT        NOT NULL DEFAULT 'dingtalk',
    phase             TEXT        NOT NULL CHECK (phase IN ('ACCEPTED', 'CONFIRMED')),
    success           BOOLEAN     NOT NULL DEFAULT false,  -- 只有 phase=CONFIRMED 时有意义
    error_code        TEXT        NOT NULL DEFAULT '',
    retryable         BOOLEAN     NOT NULL DEFAULT false,
    external_task_id  TEXT        NOT NULL DEFAULT '',     -- 钉钉的 task_id
    attempted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, attempted_at)
) PARTITION BY RANGE (attempted_at);
CREATE INDEX delivery_attempts_record_idx ON delivery_attempts (record_id, attempted_at DESC);

-- ⚠️ 初始分区覆盖当前月起 1 个月（归档窗口是全系统最短的，设计计划 §7：
-- 投递尝试是纯排障数据，权威的通知历史在 infra-notification 那边）。
CREATE TABLE delivery_attempts_2026_09_01 PARTITION OF delivery_attempts
  FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE delivery_attempts_2026_10_01 PARTITION OF delivery_attempts
  FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
ALTER TABLE delivery_attempts OWNER TO integration_im_dingtalk_rw;
