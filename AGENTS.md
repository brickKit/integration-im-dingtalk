# integration-im-dingtalk · AI 助手导读

## 身份证

| 项 | 值 |
|---|---|
| 组件 ID | `integration/im-dingtalk` |
| 仓库名 | `integration-im-dingtalk` |
| 端口 | HTTP `8207` / gRPC `9207`（`registry/ports.tsv`，装配仓库根目录那份） |
| schema / role | `integration_im_dingtalk` / `integration_im_dingtalk_rw`（归档窗口全系统最短：1 个月） |
| 语言 / 框架 | Go：Gin + `database/sql` + `pgx/v5/stdlib` + `golang-migrate` |
| 合并部署时进 | 外壳三 `go-infra` |
| 装配角色 | **`channel:im`**（族内成员：钉钉/企业微信/飞书/Slack/Teams，可多装并存，不互斥） |
| 设计真相源 | 装配仓库 `docs/design/integration-im-dingtalk.md`——本文件与它冲突时，以那份为准，回来改这里 |

⚠️ **2026-09-09 更新：用户提供了真实钉钉个人团队凭据，补了一轮真机验证**——建仓库时（03-阶段三 Task 9）没有可用的钉钉沙盒，只做到"能编译通过、内部逻辑用 `httptest` 模拟验证过"的程度；这次真机验证发现并修了两个真实 bug：① `GetSendResult` 假设的响应字段（`status`/`progress_in_percent`）根本不存在，真实响应是把收件人分到几个名单里，导致投递永远卡在 `ACCEPTED`；② `ClassifyError` 把"应用未开通所需权限"误判成可重试。**已验证通的路径**：`gettoken`（真实 Client ID/Secret 换到真实 access_token）、`topapi/v2/user/getbymobile`（真实手机号查到真实 userid）、`asyncsend_v2`（真实提交成功拿到真实 task_id）、`getsendresult`（真实响应确认已读）——四个接口的请求构造/响应解析全部核对过真实数据。**仍未验证**：钉钉的限流/频控行为、`getbymobile`/`asyncsend_v2` 之外权限缺失时的具体表现、除 `errcode=88` 外的其余错误码分类是否准确——`ClassifyError` 里没被真机验证过的分支仍是按公开文档/常识猜的。

## 边界

**归我：** 消费 IM 派发事件、手机号→钉钉 userid 的解析、钉钉密钥与 access_token 的缓存刷新、调钉钉 API 发工作通知并把结果发回。

**不归我：**

| 什么 | 归谁 | 为什么 |
|---|---|---|
| "该不该发这条通知"、"发给谁" | `infra-notification` | §6.9：适配器严禁包含业务逻辑，收到派发事件就发，不判断 |
| 文案渲染 | `infra-notification` | 我收到的是渲染好的文本 |
| 重试策略（隔多久、重试几次） | `infra-notification` | 我只报告成败与 `retryable`，重不重试它定 |
| "手机号 → userid" 的通用抽象 | 没有——这是钉钉特有的 | 每家 IM 平台认的收件人标识都不一样，通用意图事件只给稳定的自然标识（手机号），各适配器自己翻译 |

**`data_scopes: none`**——本组件没有面向用户的数据，只有运维排障用的投递记录。

## 契约面与事件

**gRPC `integration.im.v1.ImChannelService`（族级，包名 `integration.im` 不带 `dingtalk`）：** `BatchGetDeliveryStatus`（防 N+1）、`GetChannelHealth`（外部平台通不通，给运维看）。**没有发消息的 rpc**——给了它，`infra-notification` 或业务组件就可能同步调本组件，而对 `channel:*` 族成员建依赖边等于要求这个通道必须装。

**REST：** `/integration/im/**`（族级前缀，不是 `/integration/im-dingtalk/**`）。`GET /admin/deliveries`/`POST /admin/token/refresh`，权限键统一 `integration.im.admin`。

**发布事件：** `integration.im.result.v1`（核心，族级 subject，`adapter` 字段区分是谁发的，`phase` 区分 ACCEPTED/CONFIRMED，`Event.Version = attempt*10 + phase_ordinal`）。

**消费事件：** `infra.notification.dispatch.im.v1`（族级，先判 `target_adapters[]` 里有没有 `"dingtalk"`，没有就直接丢弃——NATS 核心是广播，族内每个适配器都会收到每一条）。

## 依赖与「为什么不依赖某某」

`dependencies.components` 永远是空数组，零出边（对组件而言，唯一的对外调用是钉钉的公网 API）。

- **不依赖 `infra-notification`**：消费它的事件、发结果事件回去，双向都是事件，同步调它没有任何理由。
- **不依赖 `infra-iam-casdoor`**：手机号由 `infra-notification` 放进派发事件 payload 里给我，我不去查用户中心——多一条到 iam 的边就等于给 `slot:iam` 加了一个依赖方。
- **`infra-notification` 不依赖我**：它不知道、也不该知道装了哪几个 `channel:im` 成员，谁装了就消费谁发的族级事件。

## 这个组件特有的坑

| 不许 | 症状 | 出处 |
|---|---|---|
| 消费 `dispatch.im.v1` 时省略 `target_adapters` 自过滤 | 装了多个 IM 通道的客户每条通知收到多遍——NATS 核心是广播，族内每个适配器都订阅同一个 subject | `backend/internal/consumer/consumer.go` `dispatchHandler` |
| 让 `tokenmgr.Refresh` 在拿到锁后重新检查缓存有效性再决定要不要真的调钉钉 | **真实踩过的 bug**：管理员点 `POST /admin/token/refresh` 想强制刷新，如果缓存恰好没过期，`Refresh` 会跳过调用直接返回旧 token——"强制刷新"变成了"有时候刷新"。这个"重新检查"的优化只属于 `EnsureValid`，`Refresh` 必须无条件调 | `backend/internal/tokenmgr/tokenmgr.go` |
| 重试重发/多阶段结果事件用固定的 `Event.Version` | 同一个 `record_id` 在 `integration.im.result.v1` 上的所有结果事件共用 `(subject, aggregate_id)`，`event_inbox` 按 version 严格递增去重，version 不递增会被 `infra-notification` 的 `event_inbox` 当重复消息静默吞掉 | `backend/internal/consumer/consumer.go` `publishResultTx` |
| 把延迟回查 `getsendresult` 放进消费 `dispatch.im.v1` 的同一个事务里 | `time.Sleep` 几秒会一直占着数据库连接和这条消息的 inbox 声明，拖住整个消费循环的吞吐 | `backend/internal/consumer/consumer.go` `confirmLater`（独立 goroutine，独立事务） |
| 建 `command_idempotency` 表 | 全项目核对后确认这张表只用于外部调用方提供 `idempotency_key` 的写命令 RPC，本组件没有任何写 RPC，事件消费幂等完全由 `event_inbox` 负责——从一开始就没建，别加回来 | `migrations/002_create_outbox_inbox.up.sql` 顶部注释 |
| 给 `dingtalk_user_map` 开对外查询接口，或把手机号塞进事件 payload | 手机号是个人信息，这张表只在本组件内部使用（设计计划 §2） | `migrations/001_create_dingtalk.up.sql` |
| 以为 `ClassifyError`/`GetSendResult` 的每一条分支都真机验证过 | 2026-09-09 用真实钉钉个人团队验证过一部分（见本文件顶部 ⚠️⚠️ 的更新记录），但不是全部——没验证过的分支仍是按公开文档/常识猜的，改动前先看清楚这一条命中的是哪一类 | `backend/internal/dingtalk/classify.go`、`client.go` |
| 假设 `GetSendResult` 的响应里有 `status`/`progress_in_percent` 字段 | **真实踩过的 bug**：真实响应根本没有这两个字段（是直接把收件人分到 `read_user_id_list`/`unread_user_id_list`/`failed_user_id_list`/`invalid_user_id_list`/`forbidden_list` 几个名单里，`forbidden_list` 也不是猜测的 `forbidden_user_id_list`）。按 `status>=2` 判"已完成"时字段不存在、零值恒为 0，会让延迟回查永远判不成"已完成"，一条已经真实送达的消息在 `notification_records` 里卡死在 `ACCEPTED` 直到轮次耗尽——真机测试第一次就复现了 | `backend/internal/dingtalk/client.go` `GetSendResult`/`SendResult` |
| 新写的消费者测试直接往生产 subject（如 `infra.notification.dispatch.im.v1`）发裸事件 | **真实踩过的坑**：`consumer_test.go` 原来直接往本机共享的 NATS（`nats://localhost:4222`）发布到生产 subject 字面量；NATS 核心是广播，如果那时本组件的真实容器同样在跑（`brickkit up` 起着、配了真实钉钉凭据），会把测试发的事件也当真事件处理一遍，真的调一次钉钉 API（曾经在真机验证期间意外触发过一次真实 `asyncsend_v2` 调用，因为撞上一条陈旧的 fake 缓存 userid 才没有真的打扰到人）。**已修复**：`consumer_test.go` 现在统一用 `testSubject()` 造一个带随机后缀的测试私有 subject，真实容器只订阅生产 subject 字面量、收不到测试事件，不再需要"测前手动 docker stop 掉真实容器"这道人肉工序——新写的消费者测试必须延用这个模式，不要图省事直接写生产 subject 字面量 | `backend/internal/consumer/consumer_test.go` 的 `testSubject`/`runConsumeFor` |

## 改代码前的自查

1. **我是不是在给消费 `dispatch.im.v1` 的路径省略 `target_adapters` 判断？** 停下——族级 subject 是广播，漏了这一步会重复投递。
2. **我改的 `tokenmgr.Refresh`，是不是又加回了"缓存没过期就跳过"的判断？** 停下——那是本组件出现过的真实 bug，`Refresh` 必须无条件调用。
3. **我改的结果事件发布逻辑，`Event.Version` 是不是还在用 `attempt*10+phase_ordinal` 算？** 停下并核对——version 不递增会被下游吞掉。
4. **我是不是在往消费事件的同一个事务里加一段会阻塞几秒的逻辑（比如延迟回查）？** 停下——那类逻辑必须放进独立 goroutine + 独立事务。
5. **我是不是在给某个写路径加 `command_idempotency`？** 停下——本组件没有写 RPC，不需要它。
6. **我改的错误分类/API 字段名，是不是当成"已经验证过是对的"在用？** 停下——本组件从未真机验证钉钉，所有形状都是按公开文档写的最佳猜测，改动前先说清这一点。
