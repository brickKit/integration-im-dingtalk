# itest/：集成测试与测试索引

钉钉渠道成员对着真实的 PostgreSQL 与消息系统、经它真实的接口的测试（L2）。构建标签 `integration`。

## 怎样跑

- `make test-integration`（`go test -count=1 -tags integration ./itest/`）；环境变量 `BE_SDK_TEST_PG_ADMIN`、`BE_SDK_TEST_NATS_URL`（装配项目的 `itenv.sh`）。
- 测试每次删掉再建 schema `integration_im_dingtalk_it`；端口 28207、9207 要空着。
- 钉钉是测试换进去的假钉钉（`fakeDingTalk`：记下发来的卡片，认得几个 OAuth 授权码，下一次发送答什么由测试设）；回调由测试按钉钉的方案签名与加密；通知组件由测试经 mTLS 扮演（`infra/notification`），`erp/purchase` 扮演不许调用的组件。钉钉的真实接口（`internal/dingtalk` 的 `Client`）不在这里测，在格里由 L3 对模拟钉钉测。
- **只有一个测试** `TestDingTalkEndToEnd`，各步的顺序是 `bindings`、`deliveries`、`sending`、`callbacks`、`expiry`。

## 文件

| 文件 | 步 | 测的流程 | 规则 |
| --- | --- | --- | --- |
| `flows_integration_test.go` | `bindings` | 本人经 OAuth 绑定、`state` 只用一次、伪造的 `state`、钉钉用户已绑给别人答 `taken`、本人查看与解除、管理员列出与结束、没有设置绑定的路由（501）、`state` 不存原文 | ID10；P20.9 |
| `flows_integration_test.go` | `deliveries` | 受理、同一投递再来答一样、内容不同答 `IDEMPOTENCY_KEY_REUSED`、没有绑定答 `unmapped`、别的组件不许调用、坏链接被拒 | ID01；P20.1 |
| `flows_integration_test.go` | `sending` | 卡片发给绑定的钉钉用户、动作按钮带随机值（库里只有摘要）与链接按钮、结果事实 `sent`；钉钉拒绝记 `rejected` | ID02–ID04；P20.2、P20.5 |
| `flows_integration_test.go` | `callbacks` | 回调答加密的 `success`、发一条动作事件（受众、主体、授权、动作）、重推只处理一次、原文里没有随机值；签名不对、时间戳过旧丢弃不写库；转发给别人、没有绑定、随机值被改的进隔离区并列出 | ID06–ID09；P20.6 |
| `flows_integration_test.go` | `expiry` | 结果未知的命令不重发，到放弃时刻记 `expired` | ID03；P13.14 |

共用的：

| 文件 | 内容 |
| --- | --- |
| `endtoend_integration_test.go` | `TestDingTalkEndToEnd`；`fakeDingTalk`；`fixture`（运行中的成员、Alice 全部操作、Bob 与 Carol 绑定自己的、Dave 没有授予、通知组件与别的组件的系统面客户端）；`start`；`dial`；`callback`（按钉钉的方案签名与加密）；`outbox`；`client.do`、`portFree` |

## 加测试

新流程加一个文件或一步，在上表加一行；测试不为了通过而改。
