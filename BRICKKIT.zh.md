# integration/im-dingtalk

## 组件定位

`channel:im` 族里的钉钉成员：把通知中心的 IM 派发事件变成钉钉工作通知，再把结果报回去。它不含业务逻辑：让发什么就发什么、让发给谁就发给谁，只判断一次失败值不值得重试；该不该通知、通知谁、隔多久重试几次，都归通知中心。

**拥有**

- 消费 `infra.notification.dispatch.im.v1`，只处理 `target_adapters` 里有 `dingtalk` 的事件。
- 手机号 → 钉钉 `userid` 的翻译（`topapi/v2/user/getbymobile`），缓存在 `dingtalk_user_map`；钉钉说用户不存在时删掉缓存行。
- 钉钉应用的凭据与 `access_token`：缓存在库里，过期前 5 分钟就刷新；钉钉说 token 过期时自己再刷新一次。
- 发文本工作通知（`asyncsend_v2`）并确认结果（`getsendresult`）：每次投递有 `ACCEPTED` 与 `CONFIRMED` 两个阶段，各记一条 `delivery_attempts`、各发一条 `integration.im.result.v1`。
- 判断一次失败可不可重试（钉钉的错误码只有钉钉适配器认得）。

**不拥有**

- 发不发、发给谁、文案、用户偏好、重试策略（隔多久、几次）：`infra/notification`。
- 用户与手机号：在 `infra/iam-casdoor`；通知中心把手机号放进派发事件，本组件从不去查用户。
- 别的 IM 通道（企业微信、飞书、Slack、Teams）：同一族的其它成员，与本组件共用同一套契约。

## 部署前准备

- **PostgreSQL**：schema `integration_im_dingtalk`，以及按你的 schema 约定建的 `integration_im_dingtalk_archive`（目前没有任何东西写它）。登录角色 `integration_im_dingtalk_rw`，在 `integration_im_dingtalk` 上有 `USAGE` 与 `CREATE`，以及它的密码。迁移以这个角色运行并建表，所以表归它所有；组件运行时自己建 `event_outbox` / `event_inbox`（按周）与 `delivery_attempts`（按月）的未来分区，需要这份所有权。BrickKit 不建这些；在 BrickEnterprise 装配项目里由 `make dev-env` 把密码写进 `.env`、`make db-init` 建 schema、角色与授权。
- **NATS** 在 `NATS_URL` 可达：派发事件经 NATS 核心订阅进来（组件停着时发布的消息事后不会补投）；结果经 outbox 表与后台推送发出，NATS 不可达时在 outbox 里等着。
- **一个钉钉企业内部应用**，有 AppKey、AppSecret、AgentId，并开通"通过手机号获取用户"（`qyapi_get_member_by_mobile`）与发送工作通知的权限。没开通前一项时，凡是手机号还没缓存过的投递都失败、不可重试，错误码 `DINGTALK_88`。
- **出网 HTTPS**：组件所在的地方要能访问 `DINGTALK_BASE_URL`（默认 `https://oapi.dingtalk.com`）。完全不能出网的环境不该装本组件（`channel:im` 成员本来就可装可不装），而不是装上去让每次投递都失败。
- 没有真实钉钉值时（本地或测试环境）组件照常启动、正常运行；每次投递在向钉钉申请 token 时失败，记成一条 `CONFIRMED` 失败（不可重试，`DINGTALK_40096`：AppKey 或 AppSecret 不合法）并照样发出结果事件。不会崩溃；本组件自己不重试任何东西（要不要重试由通知中心看 `retryable` 决定）。
- **授权**（`infra/authz`）与**身份**（`infra/iam-casdoor`，或任何提供 JWKS 的 IAM）在 `AUTHZ_BUNDLE_URL`、`IAM_JWKS_URL` 可达，两个管理用 REST 路由才不只是返回错误。它们是配置，不是依赖：没有它们组件照样启动。

## 依赖说明

没有依赖。本组件只活在事件上：消费通知中心的派发事件、把结果事件发回去，所以从不同步调用通知中心；也没有人调它来发消息（没有发消息的 rpc）。对 `channel:*` 成员建一条依赖边，就等于要求这个通道必须装；没有依赖边，一个项目装哪几个 IM 通道只由 `brickkit.yaml` 决定。钉钉开放平台是经 `DINGTALK_BASE_URL` 访问的外部服务，不是组件。

授权 bundle 与 JWKS 从配置里的 URL 拉取，不经依赖边。拿不到时每个受保护路由都失败关闭：没有 token 或 token 无效 → `401`；`IAM_JWKS_URL` 为空或不可达 → `403`；bundle 还没拉到过 → `503`；合法用户但没有权限键 → `403`。`/healthz` 始终是 `200`：它只报本进程活着，从不反映钉钉可不可达。

## 配置指南

| 变量 | 含义 |
|---|---|
| `PG_HOST` | PostgreSQL 主机，通常用项目共享值（`$var:PG_HOST`）。 |
| `PG_PORT` | PostgreSQL 端口；默认 `5432`。 |
| `PG_DATABASE` | 存放 `integration_im_dingtalk` schema 的库（`$var:PG_DATABASE`）。 |
| `PG_USER` | 登录角色，写字面量 `integration_im_dingtalk_rw`。进外壳后外壳以自己的角色登录、每个事务 `SET LOCAL ROLE` 切到这个角色，所以角色名必须是 `<PG_SCHEMA>_rw`。 |
| `PG_PASSWORD` | `PG_USER` 的密码。密钥：写 `${INTEGRATION_IM_DINGTALK_DB_PASSWORD}`（或你的密钥库引用），绝不写值。 |
| `PG_SCHEMA` | 全部表、outbox 与迁移状态表（`schema_migrations_integration_im_dingtalk`）所在的 schema；默认 `integration_im_dingtalk`。仍然写出字面量，让每个组件的 schema 在一处可见。 |
| `NATS_URL` | NATS 服务器：派发事件从这里进来，outbox 推送往这里发结果（`$var:NATS_URL`）。 |
| `OTEL_BASE_URL` | OpenTelemetry collector 的基地址；空（默认）表示不导出。 |
| `AUTHZ_BUNDLE_URL` | 权限判定轮询的授权 bundle 地址，例如 `http://infra-authz-2-0-0:8223/authz/bundle`。必填：没有它管理路由回 `503`。与项目里 authz 的版本保持一致。 |
| `IAM_JWKS_URL` | 本地验用户 token 用的 JWKS 地址，例如 `http://infra-iam-casdoor-2-0-0:8200/.well-known/jwks.json`。必填：没有它管理路由回 `403`。 |
| `DINGTALK_BASE_URL` | 钉钉开放平台的基地址；默认 `https://oapi.dingtalk.com`。只有走企业代理或测试替身时才改，平时不配。 |
| `DINGTALK_APP_KEY` | 钉钉企业内部应用的 AppKey。是标识不是密钥；通常仍写成 `${…}`，让三个钉钉值一起放在密钥库里。 |
| `DINGTALK_APP_SECRET` | 同一个应用的 AppSecret。密钥：写 `${INTEGRATION_IM_DINGTALK_DINGTALK_APP_SECRET}`（或你的密钥库引用），绝不写值。 |
| `DINGTALK_AGENT_ID` | 同一个应用的 AgentId，整数；工作通知以它的名义发出。不是整数时组件启动即失败，报错点名这个键。 |

## 契约索引

- `contracts/integration/im/v1/im.proto` — gRPC `integration.im.v1.ImChannelService`，每个 `channel:im` 成员都实现的族级契约：`BatchGetDeliveryStatus`（每个 `record_id` 的当前状态，`CONFIRMED` 优先于 `ACCEPTED`，按请求顺序返回，查不到的省略）与 `GetChannelHealth`（手头有没有有效 token——缓存里的或现在向钉钉申请的——何时过期、最近的错误；给运维看，不给业务判断用）。没有发消息的 rpc。Go 包是独立模块 `github.com/brickKit/integration-im-dingtalk/gen/integration/im`。
- `contracts/im-dingtalk.openapi.yaml` — REST 挂在族级前缀 `/integration/im` 下（不是 `/integration/im-dingtalk`），两个路由都要 `integration.im.admin`：`GET /admin/deliveries`（投递尝试，可按 `record_id`、`phase` 过滤，`cursor` / `page_size` 翻页：1–200，其余取 50）与 `POST /admin/token/refresh`（无条件立刻换一个新的 `access_token`）。
- `contracts/events/im-dingtalk.events.json` — 经 outbox 发布 `integration.im.result.v1`，每个阶段各一条（钉钉受理任务时 `ACCEPTED`，查到真实结果时 `CONFIRMED`），带 `record_id`、`adapter: dingtalk`、`phase`、`attempt`、`success`、`error_code`、`retryable`、`external_task_id`。version 是 `attempt × 10 + phase`（`ACCEPTED` = 1、`CONFIRMED` = 2），跨阶段、跨重试都递增。消费 `infra.notification.dispatch.im.v1`（`record_id`、`attempt`、`target_adapters`、`recipient_phone`、`body`；`title` 不发送，文本工作通知只带 `body`）。
- `assembly.yaml` — 本项目的元数据：权限键 `integration.im.admin`、菜单项、网关路由 `/integration/im/**`、schema 与角色、`data_scopes: none`。

## 外壳声明

不是外壳。可以由 Go 外壳托管（在 BrickEnterprise 项目里是 `be/go-infra`），也可以独立运行；两种方式代码相同。
