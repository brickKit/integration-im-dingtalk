# integration/im-dingtalk

## 组件定位

钉钉渠道成员：
- **投递**：通知组件经共享系统面 `be.channel.v1.ChannelService` 交来的投递，作为互动卡片发给收件人绑定的钉钉用户（卡片上有动作按钮与“在系统中处理”的链接）；钉钉不回执送达，结果只到“已发送”。
- **按钮回调**：钉钉推来的签名、加密的回调经验证后，作为“某人对某动作授权点了某按钮”的动作事件发到本组件的流上；任务所属组件（例如采购的审批）按配置订阅、核对后执行。本组件从不调用所属组件，也不记录决定（D68）。
- **本人绑定**：用户在系统里登录后经钉钉授权，把自己的钉钉账号绑到平台账号；管理员只能结束绑定，不能设置。

**3.0.2**：钉钉提前作废了应用的访问令牌（旧接口答 40014、42001）时，绑定丢掉缓存的令牌、重取后再试一次（以前要等缓存到期，其间绑定都失败）；取不到用户的网络故障答依赖不可用，不再说成“不在组织里”。种子等授予到位的方式改对（读的路由答 200 才算到位）。

**3.0.1**：3.0.0 的清单漏了 `release.goModule: true`，只打了裸标签，外壳取不到 Go 模块；补上，行为不变。

**3.0.0**（第一个重建的版本）：上述全部。钉钉的接口按公开文档建模，接真实钉钉前要用它的测试企业复核（设计的未决问题）；卡片的更新与撤回、Stream 模式以后做。

## 部署前准备

- 数据库：PostgreSQL 16 及以上；schema `integration_im_dingtalk`，属主角色与运行角色由部署方建好。
- 消息系统（`BUS_URL`）与实现实例：本组件在流 `integration.im_dingtalk` 上发动作事件与投递结果。
- 钉钉应用：`DINGTALK_APP_KEY`、`DINGTALK_APP_SECRET`（密钥）、互动卡片模板 `DINGTALK_CARD_TEMPLATE_ID`；事件订阅的 `DINGTALK_CALLBACK_TOKEN` 与 `DINGTALK_CALLBACK_AES_KEY`（密钥，43 个 base64 字符）。
- **对外入口**：`POST /api/im-dingtalk/callback`（钉钉的回调）与 `GET /api/im-dingtalk/bindings/callback`（绑定的回跳，`DINGTALK_OAUTH_REDIRECT` 是它的公开地址）要经边缘暴露到钉钉与浏览器能到达的地址；`DINGTALK_LINK_BASE` 是前端地址（卡片里的链接相对它），`DINGTALK_BIND_RETURN` 是绑定后回到的前端页面。部署不能从外网到达时，按钮与绑定都不起作用，卡片照发。
- **通知组件**：在它的 `NOTIFICATION_CHANNELS` 里配 `dingtalk=integration/im-dingtalk`；本组件的投递只许 `infra/notification` 调用。
- **任务所属组件**：要在钉钉里点按钮，所属组件（`erp/purchase`、`erp/payment`）的 `FLOW_ACTION_SOURCES` 列出本组件；没列时卡片只有链接按钮。
- **不与不受信任的成员同壳**：同一外壳的成员共用总线连接，能在本组件的流上发布动作事件；放基础外壳（`be/go-infra`）或单跑。
- 授权：`integration.im_dingtalk.my_binding.manage` 给要绑定钉钉的用户；`integration.im_dingtalk.binding.read`、`integration.im_dingtalk.binding.end`、`integration.im_dingtalk.callback.read` 给维护的人。

## 依赖说明

- 必需：数据库、消息系统、身份成员、授权成员、钉钉。
- 上游：`infra/notification` 调用本组件（它在自己的清单里声明本组件为可选依赖）。
- 下游：任务所属组件订阅动作事件（不声明依赖）。

## 配置指南

全部键见 `component.yaml` 的 `configSchema`（由 SDK 生成，测试核对）。

| 键 | 说明 |
| --- | --- |
| `DINGTALK_API_URL`、`DINGTALK_OAPI_URL`、`DINGTALK_LOGIN_URL` | 钉钉的新接口、旧接口与授权页（默认是钉钉的公网地址；格里指向模拟钉钉） |
| `DINGTALK_APP_KEY`、`DINGTALK_APP_SECRET`（必填） | 钉钉应用；秘钥是密钥 |
| `DINGTALK_CARD_TEMPLATE_ID`（必填） | 互动卡片的模板 |
| `DINGTALK_CALLBACK_TOKEN`、`DINGTALK_CALLBACK_AES_KEY`（必填，密钥） | 回调的签名与加密 |
| `DINGTALK_LINK_BASE`、`DINGTALK_OAUTH_REDIRECT`、`DINGTALK_BIND_RETURN`（必填） | 前端地址、绑定回跳的公开地址、绑定后回到的页面 |
| `PG_HOST`、`PG_DATABASE`、`PG_USER`、`PG_PASSWORD`、`PG_SCHEMA`（必填） | 自己的数据库，密码是密钥；迁移另用 `PG_OWNER_USER`、`PG_OWNER_PASSWORD` |
| `BUS_URL`（必填，密钥）、`IMPLEMENTATION_INSTANCE`（必填） | 消息系统；实现实例是安装时生成的随机身份 |
| `DEPLOYMENT_ID`、`IAM_ISSUER`、`IAM_JWKS_URL`、`AUTHZ_GRPC_URL`、`SERVICE_TLS_CERT_FILE`、`SERVICE_TLS_KEY_FILE`、`SERVICE_TLS_CA_FILE`（必填） | 受保护的用户面与 mTLS 的系统面 |
| `EXTERNAL_COMMANDS_QUERY_ONLY` | 只查询、不发送（客户部署无法从外部到达时；线协议 P13.10） |

## 契约索引

- **系统面**：线协议的共享契约 `be.channel.v1.ChannelService/Deliver`（SDK 的 `channelpb`），操作 `integration.im_dingtalk.system.deliver`，只许 `infra/notification`。答 `accepted` 或 `unmapped`（这个人没有绑定钉钉）。对外命令的对账 `be.external.v1.ReconcileService`（只许恢复工具）。
- **用户面**：`contract/openapi.yaml`。公开路由两条（回调、绑定的回跳）；`POST /api/im-dingtalk/bindings/start`、`GET`/`DELETE /api/im-dingtalk/bindings/me`（`integration.im_dingtalk.my_binding.manage`）；`GET /api/im-dingtalk/bindings`（`integration.im_dingtalk.binding.read`）、`DELETE /api/im-dingtalk/bindings/{principal}`（`integration.im_dingtalk.binding.end`）；`GET /api/im-dingtalk/callbacks`（隔离区，`integration.im_dingtalk.callback.read`）。
- **事件**：发布 `integration.im_dingtalk_action.recorded.v1`（线协议的共享 schema `schemas/channel/action.v1`：受众、主体、动作授权、动作、点击的时刻）与 `integration.im_dingtalk_delivery.recorded.v1`（`schemas/channel/delivery.v1`：`sent`、`failed`、`rejected`、`expired`）；副本在 `contract/events/`。不订阅。

## 外壳声明

不是外壳。可以被 Go 的基础外壳（`be/go-infra`）承载。
