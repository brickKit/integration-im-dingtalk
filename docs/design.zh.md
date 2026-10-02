[English](design.md) · [中文](design.zh.md)

# integration/im-dingtalk 设计

只写结论，给要改本组件设计的人。怎么用：`BRICKKIT.zh.md`；代码怎么布局：`AGENTS.zh.md`。

## 边界

一个 Channel Adapter 意义上的适配器：监听事件、调外部 API、发结果事件，不含业务逻辑。它是 `channel:im` 族（钉钉、企业微信、飞书、Slack、Teams）的第一个成员，族级契约由它定形：它长什么样，后面的成员就得长什么样。

| 不在这里 | 归谁 | 为什么 |
|---|---|---|
| 该不该发、发给谁、用户偏好 | `infra/notification` | 适配器收到什么就发什么；做判断就是业务逻辑 |
| 消息文案 | `infra/notification` | 派发事件里带的是渲染好的文本 |
| 重试策略（隔多久、几次） | `infra/notification` | 两边都重试，次数相乘（外面三次 × 里面三次 = 九条消息） |
| 用户与手机号 | `infra/iam-casdoor` | 通知中心把手机号放进派发事件；在这里查，就给 IAM 槽位多加一个依赖方 |
| 邮件、短信、站内信 | 各自的通道 | 本组件只管钉钉 |

只在这里、别处没有的：手机号 → 钉钉 `userid` 的翻译、钉钉凭据与 `access_token`、判断一次钉钉失败值不值得重试。手机号翻译放在适配器里，是因为它是钉钉特有的：每个 IM 平台认的收件人标识都不一样（钉钉认 userid、Slack 认 member ID、飞书认 open_id），所以族级契约只带一个稳定的自然标识（手机号），由各适配器自己翻译。

## 拥有的数据

| 表 | 分区 | 说明 |
|---|---|---|
| `dingtalk_user_map` | 否 | 手机号（主键）→ `userid`、`resolved_at`。是缓存：getbymobile 有频控，不能每发一条查一次。存的是明文手机号（调钉钉要用，没法哈希）：没有接口读它，也不进任何事件 |
| `dingtalk_token` | 否 | 单行（`id = 1`，`CHECK` 约束）：`access_token`、`expires_at`。落库而不是只放内存：每次重启都重新申请会撞 gettoken 的频控 |
| `delivery_attempts` | 按月，`attempted_at` | 每次尝试的每个阶段各一行，写入即终态、不更新：`record_id`、`phase`（`ACCEPTED` / `CONFIRMED`）、`success`、`error_code`、`retryable`、`external_task_id`（钉钉的 `task_id`，回查结果的钥匙）。索引 `(record_id, attempted_at DESC)` |
| `event_outbox`、`event_inbox` | 按周，`created_at` | 标准的两张：结果经 outbox 发出；inbox 让消费派发事件幂等 |

没有 `command_idempotency` 表：它服务于带调用方 `idempotency_key` 的写 rpc，而本组件没有写 rpc。唯一的写入路径是消费派发事件，由 `event_inbox` 保证幂等。

手机号缓存靠剔除过期，不靠时间：钉钉说用户不存在或不在企业内时删掉这一行，下次投递重新查（人会离职、换号）。定时全量刷新会把频控吃光。

## 契约面

gRPC `integration.im.v1.ImChannelService`——包名是族级的 `integration.im`，不是 `integration.im.dingtalk`：每个成员实现同一份 proto，项目换通道时调用方不改 import。

| rpc | 类型 | 说明 |
|---|---|---|
| `BatchGetDeliveryStatus` | 读 | 排障用的批量读：每个 `record_id` 的当前状态（`CONFIRMED` 优先于 `ACCEPTED`，再取最新），按请求顺序返回，查不到的省略 |
| `GetChannelHealth` | 读 | 手头有没有有效 token、何时过期、最近的错误。给运维看，不给业务判断用；这是唯一探测钉钉的地方，而且它不是 `/healthz` |

刻意没有发消息的 rpc。有了它，通知中心或业务组件就可能同步调本组件，而对 `channel:*` 成员建依赖边，就等于要求这个通道必须装。消息只能经事件进来。

REST 挂在族级前缀 `/integration/im` 下（不是 `/integration/im-dingtalk`），两个都要 `integration.im.admin`：

| 路径 | 说明 |
|---|---|
| `GET /admin/deliveries` | 排障：一条通知发出去没有、钉钉回了什么；按 `record_id`、`phase` 过滤；按 ID 游标翻页 |
| `POST /admin/token/refresh` | 应急：不管缓存怎样，立刻申请一个新的 `access_token` |

没有 webhook 入口：钉钉工作通知是单向推送，不需要回调地址。

## 事件

消费：

| subject | 来自 | 做什么 |
|---|---|---|
| `infra.notification.dispatch.im.v1` | `infra/notification` | 先看 `target_adapters` 里有没有 `dingtalk`，没有就丢弃——族内每个成员订阅这同一个 subject，NATS 核心订阅投给所有订阅者，漏掉这一步，装了几个通道每条通知就送达几次。有就解析 userid、发送、记录并发布 `ACCEPTED`、回查结果、记录并发布 `CONFIRMED`。经 `event_inbox` 按 `(subject, record_id, version)` 幂等 |

发布：

| subject | 何时 | payload |
|---|---|---|
| `integration.im.result.v1` | 每个阶段各一次 | `record_id`、`adapter: dingtalk`、`phase`、`attempt`、`success`、`error_code`、`retryable`、`external_task_id` |

subject 是族级的，成员写在 `adapter` 里：通知中心只消费一条 subject 就能处理全部 IM 通道，加一个通道它那边什么都不用改。

**钉钉的发送是异步的。** `asyncsend_v2` 返回 `task_id`，只代表任务被受理。在这一刻报成功，就是谎报一次谁也确认不了的送达，所以每次投递有两个阶段：

1. `asyncsend_v2` 成功 → 记录并发布 `ACCEPTED`（通知中心显示"已受理"，不是"已送达"）。
2. 几秒后回查 `getsendresult(task_id)`，每轮等待翻倍，最多四轮（5 秒、10 秒、20 秒、40 秒）→ 记录并发布带真实结果的 `CONFIRMED`。响应里没有进度字段：收件人出现在 `read_user_id_list` / `unread_user_id_list`（成功）、`failed_user_id_list`（失败，可重试）、`invalid_user_id_list` / `forbidden_list`（失败，不可重试）之一，或者还哪个都不在（处理中）。轮数用完时记 `CONFIRMED`，错误码 `GET_SEND_RESULT_TIMEOUT`、可重试——比永远停在 `ACCEPTED` 好。

回查在 goroutine 里、用自己的事务做，绝不在消费事务里做（在那里 sleep 会占着连接和 inbox 声明）。不能省掉第 2 步：最常见的失败"用户不在企业内"只在这一步出现，手机号缓存的剔除也靠它。

钉钉还没受理就失败的（拿不到 userid、拿不到 token、`asyncsend_v2` 被拒）直接记成 `CONFIRMED` 失败，它们没有 `ACCEPTED` 阶段。

**version 必须递增。** 同一个 `record_id` 的全部结果，跨阶段、跨重试，共用 `(subject, aggregate_id)`，通知中心的 inbox 只收严格更大的 version。version = `attempt × 10 + phase`（`ACCEPTED` = 1、`CONFIRMED` = 2），`attempt` 取自派发事件的 payload。

**`retryable` 是适配器与通知中心的分工线。** 一次钉钉错误值不值得重试只有适配器知道；要不要重试、什么时候重试由通知中心决定：

| 钉钉返回 | `retryable` | 为什么 |
|---|---|---|
| 网络错误、"系统繁忙"、限流 | 是 | 临时故障 |
| 用户不存在 / 不在企业内 | 否 | 重试也一样；同时删掉手机号缓存行 |
| AppKey / AppSecret 无效 | 否 | 配置错了；重试只会刷屏，要让运维看见 |
| 应用缺权限（errcode 88） | 否 | 要人去开发者后台开通 |
| `access_token` 过期 | — | 适配器自己刷新 token 并重试一次，不消耗通知中心的重试次数 |
| 认不出的 | 是 | 重试上限在通知中心 |

按 `errmsg` 里的关键词匹配，不按 `errcode`。只有缺权限一类见过真实响应，其余分支按钉钉公开文档推断。

## 依赖

没有，也不该有：

| 不是依赖 | 为什么 |
|---|---|
| `infra/notification` | 双向都是事件，没有需要同步调用的东西 |
| `infra/iam-casdoor` | 手机号在派发事件的 payload 里，由通知中心放进去；对 IAM 建边，就给 IAM 槽位多一个依赖方 |
| `infra/authz` | 权限 bundle 从 `AUTHZ_BUNDLE_URL` 轮询：是配置，不是依赖边 |
| 钉钉开放平台 | 外部服务，经 `DINGTALK_BASE_URL`、用配置里的应用凭据访问 |

## 在同步调用图里的位置

不在图里：没有出边；没有发消息的 rpc，也就没有入边。它活在事件图的末端，是通知链路的终点。不碰 CRM 也不碰 ERP，与三个枢纽无关。

它是少数会访问公网的组件之一。客户完全离线的私有化部署不该装它（族本来就可装可不装）；这是装配期该问清楚的事，不该等到每次投递都失败才发现。

## 分区与归档

| 数据 | 热 | 之后 |
|---|---|---|
| `delivery_attempts` | 最近一个月 | 更早的整月分区该进 `integration_im_dingtalk_archive` |
| `dingtalk_user_map` | 一直 | 不归档；失效行是删除（见"拥有的数据"） |
| `dingtalk_token` | 单行 | 不归档 |
| `event_outbox`、`event_inbox` | 发布后 30 天 | 删除 |

搬走与删除旧分区还没实现（见"未决问题"），上表是目标。归档窗口是全系统最短的：投递尝试是排障数据，只在出问题后的头几天有价值；权威的通知历史在 `infra/notification`，两边都长期留是重复。

迁移只建最初的分区，其余由运行中组件的分区循环提前建好（周分区提前四周、月分区提前三个月），未来分区缺失不会让写入失败。

## 数据范围

没有（`data_scopes: none`）。本组件没有面向用户的数据，只有运维排障时读的投递尝试，挂在一个管理权限后面。

## 参考实现

| 项目 | 版本 | 看的模块 | 借鉴了什么 | 许可证 | 用法 |
|---|---|---|---|---|---|
| 钉钉开放平台 | 公开文档 | 工作通知（`asyncsend_v2`、`getsendresult`）、按手机号查用户、`gettoken` 及其限制 | 全部对外接口；请求与响应形状后来对照真实响应核对过 | 闭源平台 | 借鉴实际应用 |
| Novu | — | provider 接口（发送的入参出参、错误分类） | 哪些失败算永久失败：`retryable` 那张表的思路 | MIT | 借鉴逻辑 |
| Apache Camel | — | 企业集成模式：Message Translator、Channel Adapter | 这个角色本身，以及适配器不含业务逻辑 | Apache-2.0 | 借鉴实际应用 |

**刻意避免的**：

| 常见做法 | 这里为什么不 |
|---|---|
| 适配器内部自带重试与退避 | 重试归通知中心；两边都做次数相乘 |
| 每个通道一个 subject，通知中心逐个订阅 | 加一个通道就要改通知中心；一条族级 subject + `target_adapters` 不用 |
| 适配器去用户中心查收件人标识 | 会给 IAM 槽位加依赖方；收件人随派发事件进来 |

企业微信、飞书、Slack 的 SDK 刻意还没看：现在照着它们设计族级契约，要么做成三家的并集，要么按钉钉一家定死。契约只按最小交集"发一条文本给一个自然标识"来定，建第二个成员时再复核。

族本身就是"多种实现都合理、只是适合的客户不同"的槽位族答案（国内用钉钉或企业微信，海外用 Slack 或 Teams）。本组件内部没有别的变体。

## 未决问题

| 问题 | 目前的答案 |
|---|---|
| `confirmLater` 回查期间组件重启，那次投递永远停在 `ACCEPTED` | 已知缺口：待回查的任务没有持久化。指望通知中心把停在 `ACCEPTED` 的记录查出来；真有影响时，启动时扫描未确认的 `ACCEPTED` 行就是修法 |
| 处理派发事件时数据库出错（不是钉钉出错），这条事件就丢了 | `besdk.Consume` 用 NATS 核心订阅，handler 失败只记日志，不重投、不进死信。要至少一次送达得用 JetStream 消费者，是 SDK 层面的决定 |
| 钉钉已受理的消息，`ACCEPTED` 那一行却没提交成功 | 事务连同 inbox 声明一起回滚，结果事件没发出，通知中心重试时会再发一次。外部调用没法放进事务；作为小概率情况接受 |
| `confirmLater` 在派发事务提交之前就启动了 | 那次提交失败时，可能出现没有 `ACCEPTED` 的 `CONFIRMED` 行；第一次回查要等 5 秒，所以不太可能 |
| `delivery_attempts` 一个月后归档、旧的 outbox / inbox 分区删除 | 还没实现：没有东西搬走或删除旧分区，它们留在热 schema 里。全项目都是这样；SDK 层面的分区保留助手多半是答案 |
| `GET /admin/deliveries` 不像项目里的列表那样有默认时间窗口 | 它是管理排障视图，面对的表只打算热一个月；表变大时加 `attempted_after` / `attempted_before`（只增） |
| 交互式卡片（在钉钉里直接审批） | 没做。要一个 webhook 入口，并把钉钉回调翻译成审批动作——那是本组件第一次带上业务语义，先重新审视边界 |
| 族级契约容得下企业微信、飞书、Slack 吗？ | 建第二个成员时复核；加字段前先问其它成员能不能实现 |
