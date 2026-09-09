# integration-im-dingtalk · 钉钉通道适配器

`channel:im` 族第一个成员——消费 `infra-notification` 的族级派发事件，把手机号翻译成钉钉 `userid`，调钉钉开放平台发工作通知，把结果发回 `infra-notification`。族内契约面必须完全一致：**它长什么样，后面企业微信/飞书/Slack/Teams 就得长什么样**（设计书 §5.11）。

## 它能做什么

- 消费族级事件 `infra.notification.dispatch.im.v1`：先判 `target_adapters[]` 里有没有 `"dingtalk"`（族内五个适配器共用同一个 subject，NATS 核心是广播，漏了这一步会让装了多个 IM 通道的客户每条通知收到多遍），有则解析 userid → 调钉钉 `asyncsend_v2` → 记 `ACCEPTED` → 延迟回查 `getsendresult` → 记 `CONFIRMED`，两个阶段各发一条 `integration.im.result.v1`
- `integration.im.v1.ImChannelService`（gRPC，族级契约）：`BatchGetDeliveryStatus`（防 N+1，排障用）、`GetChannelHealth`（外部平台通不通、token 还有多久过期）
- REST（`/integration/im/**`，族级前缀）：`GET /admin/deliveries`（排障）、`POST /admin/token/refresh`（应急强制刷新）

⚠️ **没有任何"发消息"的 rpc**——给了它，`infra-notification` 或业务组件就可能同步调本组件，而对 `channel:*` 族成员建依赖边等于要求这个通道必须装，族的可装可不装当场失效。发消息只能通过事件进来。

## 需要哪些基础资源

| 资源 | 形态 | 为什么需要 | 怎么起 |
|---|---|---|---|
| PostgreSQL 16 | **A**（`kind: database`） | `dingtalk_user_map`/`dingtalk_token`/`delivery_attempts`（月分区）独占 schema `integration_im_dingtalk` | 装配仓库根目录 `make up` |
| NATS 2.10 | **A**（`kind: mq`） | 消费 `dispatch.im.v1`，发 `integration.im.result.v1` | 同上 |
| 钉钉开放平台 | 外部 SaaS，不是组件也不是基础资源 | 实际发消息的地方 | 地址/密钥走 `configSchema` |

⚠️ **本组件是全系统少数几个"会主动访问公网"的组件之一**——装了它意味着这台机器需要能出网到钉钉。客户完全内网部署时，正确做法是不装这个组件（`channel:*` 本来就可装可不装），而不是让它装上去一直失败。

⚠️ **零强依赖零弱依赖，零出边**——不依赖 `infra-notification`（双向都是事件）；不依赖 `infra-iam-casdoor`（手机号由 `infra-notification` 放进派发事件 payload 里给我，我不去查，多一条到 iam 的边等于给 `slot:iam` 加了一个依赖方）。

## 怎么起来

```bash
make up
cd components/integration/im-dingtalk
go build -o build/migrate ./backend/cmd/migrate
PG_SCHEMA=integration_im_dingtalk DATABASE_HOST=localhost DATABASE_PORT=5432 \
  DATABASE_USER=postgres DATABASE_PASSWORD=<.env 里的 POSTGRES_PASSWORD> DATABASE_NAME=brickkit_db \
  ./build/migrate up
DINGTALK_APP_KEY=... DINGTALK_APP_SECRET=... DINGTALK_AGENT_ID=... go run ./backend/cmd/server
```

或者用平台：`brickkit up`。

⚠️ **阶段三 Task 9 建仓库时没有可用的钉钉沙盒/测试企业**——用户明确决定跳过真机验证，`brickkit.yaml` 里配的是占位测试凭据，只够让组件正常启动、走通除"真的调钉钉 API"之外的全部代码路径。真实调用钉钉那四个接口（`gettoken`/`getbymobile`/`asyncsend_v2`/`getsendresult`）**从未打到真实服务器验证过**，只用 `httptest.Server` 模拟接口形状验证了本组件自己的请求构造/响应解析/错误分类逻辑。等有真实测试企业凭据后需要补一次真机验证。

## 怎么用

```bash
# 排障：查某条通知发出去没有
curl -H 'Authorization: Bearer <应用 token>' \
  'http://localhost:8207/integration/im/admin/deliveries?record_id=123'

# 应急强制刷新 access_token
curl -X POST -H 'Authorization: Bearer <应用 token>' \
  http://localhost:8207/integration/im/admin/token/refresh

# 组件间协议：查投递状态
grpcurl -plaintext -d '{"record_ids":["123"]}' \
  localhost:9207 integration.im.v1.ImChannelService/BatchGetDeliveryStatus
```

真实触发一次投递：让 `infra-notification` 真的路由一条通知（`target_adapters` 里带 `"dingtalk"`），本组件会消费 `infra.notification.dispatch.im.v1` 并尝试调钉钉——在没有真实凭据的环境下这一步会在 `asyncsend_v2` 失败，`delivery_attempts` 会记一条 `CONFIRMED` 失败，这是预期行为，不是 bug。

## 配置项

| 配置键 | 默认值 | 必填 | 说明 |
|---|---|---|---|
| `pgSchema` | `integration_im_dingtalk` | 否 | 本组件的 PG schema |
| `otelBaseUrl` | `""` | 否 | 空 = Blackhole Exporter |
| `iamJwksUrl` / `authzBundleUrl` | `""` | 否 | JWT 验签 / 权限 bundle |
| `dingtalkBaseUrl` | `https://oapi.dingtalk.com` | 否 | 换成企业内部代理/测试环境时改这个 |
| `dingtalkAppKey` / `dingtalkAppSecret` / `dingtalkAgentId` | 无 | **是** | 钉钉企业内部应用的三件套，没有它组件启动即 panic（配置缺失属于部署错误） |

### `retryable` 是适配器与通知中心的分工线

重试策略归 `infra-notification`，但"这个错误值不值得重试"只有本组件知道——钉钉的错误码是钉钉特有的。分类逻辑在 `backend/internal/dingtalk/classify.go`：网络层错误、限流/系统繁忙、区分不了的未知错误→ 可重试；用户不存在/不在企业内 → 不可重试且清缓存；AppKey/AppSecret 无效 → 不可重试；access_token 过期 → 本组件自己刷新重试一次，不消耗 `infra-notification` 的重试次数。

⚠️ **判据按 errmsg 关键词匹配，不是 errcode 数字**——没有真实钉钉沙盒验证具体错误码是否真的对应这些场景，是"只做到能编译通过的程度"这条既定决策的直接后果。等有真实测试企业时要用真实响应验证。

## 参考实现

| 项目 | 看的模块 | 借鉴了什么 | 许可证 | 用法 |
|---|---|---|---|---|
| 钉钉开放平台公开文档 | 工作通知 `asyncsend_v2`/`getsendresult`、手机号查 userid、access_token 获取 | 本组件全部的对接面——⚠️ 从未真机验证，见上方 ⚠️ | 闭源平台 | 借鉴实际应用 |
| Novu | provider 的接口形状 | 错误分类的思路（哪些算永久失败） | MIT | 借鉴逻辑 |
| Apache Camel | Enterprise Integration Patterns 的 Channel Adapter | "适配器不该含业务逻辑"是这个模式的题中之义 | Apache-2.0 | 借鉴实际应用 |

## 边界与禁令

- **没有发消息的 rpc**——唯一入口是事件，见上方 ⚠️
- **不做"该不该发"、"发给谁"的判断**——收到派发事件就发，不判断；只认 payload 里给的收件人
- **不持有重试策略**——只报告成败与 `retryable`，重不重试、隔多久、重试几次归 `infra-notification`
- **`dingtalk_user_map` 存的是明文手机号，只在组件内部使用**——不开任何对外查询接口，也不进任何事件 payload
- **`delivery_attempts` 写入即终态**——ACCEPTED 与 CONFIRMED 是同一个 `record_id` 的两条独立记录，不是同一行原地更新
- **重试重发/多阶段结果事件的 `Event.Version` 必须严格递增**（`version = attempt*10 + phase_ordinal`）——同 `infra-notification` 那次 version 单调 bug 用的是同一个教训
