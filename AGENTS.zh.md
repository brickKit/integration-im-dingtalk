[English](AGENTS.md) · [中文](AGENTS.zh.md)

# integration/im-dingtalk

开发本组件的 AI 指南。用法、边界与契约：BRICKKIT.md；为什么这样设计：`docs/design.zh.md`；依赖、配置与部署：component.yaml。

## 代码地图

| 路径 | 负责 |
|---|---|
| `backend/module/module.go` | 唯一入口 `New(ctx, rt)`：读钉钉配置（`readDingtalkConfig`，出错时一次点名全部有问题的键），装配 repo → tokenmgr → service → HTTP + gRPC，起三个后台循环：outbox 推送、分区维护、派发事件消费 |
| `backend/cmd/server/main.go` | 一行 `besdk.RunStandalone(module.New)` |
| `backend/cmd/migrate/main.go` | 一行 `migrate.Main(migrations.FS)`：迁移容器的入口（`./migrate up`） |
| `backend/internal/consumer/consumer.go` | 发消息的唯一路径：消费派发事件、按 `target_adapters` 过滤、解析 userid、发送、记 `ACCEPTED`、在 goroutine 里回查 `getsendresult`、记 `CONFIRMED`；`recordResultTx` 在一个事务里写投递尝试与结果事件 |
| `backend/internal/dingtalk/client.go` | 钉钉的四个 HTTP 调用（`gettoken`、`getbymobile`、`asyncsend_v2`、`getsendresult`），10 秒超时，网络层错误去掉 query（`withoutQuery`） |
| `backend/internal/dingtalk/classify.go` | `ClassifyError`（可不可重试、要不要清手机号缓存）与 `IsTokenExpired` |
| `backend/internal/tokenmgr/tokenmgr.go` | `access_token` 的缓存与刷新：`EnsureValid`（互斥锁下的单飞）与 `Refresh`（无条件） |
| `backend/internal/repo/` | `token.go`（单行表 `dingtalk_token`）、`usermap.go`（手机号 → userid 缓存）、`deliveries.go`（`delivery_attempts`、`BatchGetLatestStatus`、`ListDeliveries`） |
| `backend/internal/service/` | REST 与 gRPC 共用的读与管理操作；错误到 gRPC 状态码的映射（`status.go`） |
| `backend/internal/http/http.go` | `/integration/im` 下的两个管理路由，各自带 `integration.im.admin` 注册 |
| `backend/internal/grpc/grpc.go` | `integration.im.v1.ImChannelService` |
| `backend/internal/partition/` | 全部分区表共用一个循环：`event_outbox` / `event_inbox` 按周提前四周（`partition.go`），`delivery_attempts` 按月提前三个月（`monthly.go`） |
| `backend/internal/testlock/` | 只给测试用：`TokenRow` 让碰单行表 `dingtalk_token` 的测试跨测试包串行 |
| `migrations/` | SQL 迁移，由 `migrations/embed.go` 嵌入 |
| `contracts/` | proto、OpenAPI、事件 schema |
| `gen/integration/im/` | 生成的 Go 代码：嵌套的独立 Go 模块，单独打 gen/integration/im/v1.x.y 的 tag；不手改 |

| 要做的事 | 从这里看 | 然后 |
|---|---|---|
| 一条派发事件怎么变成钉钉消息 | `backend/internal/consumer/consumer.go`（`handle`） | `backend/internal/consumer/consumer_test.go`（假钉钉 + 真实 PostgreSQL 与 NATS） |
| 钉钉错误分类不对 | `backend/internal/dingtalk/classify.go` | `backend/internal/dingtalk/classify_test.go`（真实响应原文） |
| 钉钉响应解析不对 | `backend/internal/dingtalk/client.go` | `backend/internal/dingtalk/client_test.go` |
| token 刷新太频繁或不刷新 | `backend/internal/tokenmgr/tokenmgr.go` | `backend/internal/tokenmgr/tokenmgr_test.go` |
| 新增一张分区表 | `migrations/`（新迁移） | `backend/internal/partition/`（加进表清单）、`backend/internal/partition/partition_test.go` |
| 管理接口加一个查询参数 | `backend/internal/http/http.go` | `backend/internal/repo/deliveries.go`、`contracts/im-dingtalk.openapi.yaml` |

## 构建与测试

```bash
# 测试连测试库 brickkit_test_db，绝不连 brickkit_db（项目根目录 make test-db-init 建好它）
export TEST_PG_DSN="postgres://<用户>:<密码>@localhost:5432/brickkit_test_db?sslmode=disable"
export TEST_NATS_URL=nats://localhost:4222
make test                    # 每个包都是 "ok"；没设 TEST_PG_DSN 时拒绝运行
go test ./... -count=1 -v | grep -c -- '--- SKIP'   # 0：没有被跳过的测试
make check-version dag-check contract-check import-scan module-check   # 各打印一行 ✓
make docs-check              # "0 with errors, 0 warnings"
# 迁移，以登录角色运行：
PG_HOST=localhost PG_PORT=5432 PG_DATABASE=brickkit_test_db PG_USER=integration_im_dingtalk_rw \
  PG_PASSWORD=<它的密码> PG_SCHEMA=integration_im_dingtalk make migrate-idempotent   # ✓ 迁移幂等
```

钉钉的四个接口都用 `httptest` 模拟，不需要真实凭据。消费者测试发布到测试私有的 subject（`testSubject`），在跑的本组件容器收不到。真机上在项目根目录跑 `make verify ID=integration/im-dingtalk ROUTE=/integration/im/admin/deliveries FOCUS=1`：构建镜像、只起本组件需要的东西、核对迁移、健康与权限判定、跑一次 focus，然后收尾。

## 设计取舍

- **只走事件、没有依赖、没有发消息的 rpc**：`channel:*` 成员必须保持可装可不装；`make dag-check` 遇到任何依赖都失败。
- **每次投递两个阶段**：`asyncsend_v2` 成功只代表钉钉受理了任务；记 `ACCEPTED` 并发布，真实结果靠回查 `getsendresult` 得到，记 `CONFIRMED`。
- **适配器判断、通知中心重试**：本组件只报 `retryable`，自己从不重试一条派发；唯一自己重试的是刷新一次 token。
- **`EnsureValid` 与 `Refresh` 故意不同**：只有 `EnsureValid` 在拿到锁后再看一次缓存。
- **手机号缓存在"用户不存在"时清掉**，从不定时刷新（getbymobile 有频控）。
- 原因与完整契约面：`docs/design.zh.md`。

## 易错点

| 不要 | 症状 | 原因 |
|---|---|---|
| 处理派发事件时不检查 `target_adapters` 里有没有 `dingtalk` | 装了三个 IM 通道的项目，每条通知送达三次 | 族内每个成员订阅同一个 subject，NATS 核心订阅会投给所有订阅者 |
| 让 `Refresh` 在缓存 token 仍有效时跳过调用 | `POST /admin/token/refresh` 回 `200`，什么都没变 | 强制刷新必须无条件；"再看一次缓存"只属于 `EnsureValid` |
| 发布 `integration.im.result.v1` 时 version 不跨阶段、跨重试递增 | 通知中心把后到的结果当重复静默丢掉 | 它的 inbox 对每个 `record_id` 只收严格更大的 version；`recordResultTx` 里保持 `attempt × 10 + phase` |
| 在消费事务里回查 `getsendresult` | 吞吐崩掉：每条消息都占着一个连接和它的 inbox 声明好几秒 | 回查在 `confirmLater` 里做，那是派发事务之外的 goroutine，自己开事务写 |
| 按 `getsendresult` 的某个进度字段判"已完成" | 已送达的消息一直停在 `ACCEPTED`，直到回查轮数用完 | 真实响应没有进度字段，只有收件人名单 |
| 把请求 URL 或原始的 `*url.Error` 放进错误、日志或 `last_error` | 钉钉不可达时，AppSecret（gettoken 的 query）或 access_token 出现在日志与 `GetChannelHealth` 的响应里 | `withoutQuery` 只留方法与路径 |
| 往 `infra.notification.dispatch.im.v1` 发测试事件 | 在跑的本组件容器把它当真事件处理，真的调钉钉 API | 用 `testSubject`；只有生产容器监听生产 subject |
| 新增分区表却不加进 `backend/internal/partition/` | 一切都过，直到迁移建的分区用完后的第一条写入："no partition of relation found for row"，每条派发都失败 | 迁移只建最初的分区，其余靠这个循环 |
| 写碰 `dingtalk_token` 的测试却不调 `testlock.TokenRow` | `go test ./...` 下测试随机失败 | 这张表只有一行，测试包之间并行跑 |
| 在 `New` 里对坏配置 `panic` 或用 `MustString` | 进了外壳，整个外壳带着堆栈崩掉，而不是点名成员与键后退出 | 返回 error；`readDingtalkConfig` 一次收集全部有问题的键 |
| 通过接口或事件暴露 `dingtalk_user_map` | 手机号流出本组件 | 缓存里是明文手机号（调钉钉要用），只在组件内部使用 |

## 改代码前自查

1. 这是业务判断吗（要不要通知、通知谁、隔多久重试）？那归 `infra/notification`，不在这里。
2. 我是不是在加依赖、加发消息的 rpc、或加一条调用本组件的同步调用？停下：族必须保持可装可不装。
3. 改契约？这是族级契约：只增不改，并且先问每个 IM 通道能不能实现。绝不删字段、改类型、删 rpc / 路径 / subject。
4. `gen/` 变了吗？变了就要给契约包打新 tag，`go.mod` 也要 require 它。
5. 动钉钉响应解析或错误分类？说清这个分支有没有对照过真实响应（注释里标着），测试里的假服务器保持真实的形状。
6. 新规则 → 先写失败的测试（涉及 SQL 的用真实数据库），再写代码。
7. 发布后的第一个改动之前先升 `metadata.version`；BRICKKIT.md 与 `docs/design.md` 和代码在同一个提交里更新。
