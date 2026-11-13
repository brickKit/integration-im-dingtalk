# dingtalk：钉钉的接口

## 负责与不负责

- 负责：`Client`（`core.DingTalk` 的实现：应用的访问令牌缓存到过期前 5 分钟；`SendCard` 发互动卡片并分类结果——受理、连接失败与限流与令牌被拒算没发到、超时与 5xx 算结果未知、其余 4xx 算拒绝；`UserOfCode` 用 OAuth 的授权码换钉钉用户，应用的令牌被钉钉提前作废时重取、再试一次；`ConsentURL`）；`crypto.go`（回调的签名 `Sign`、`Verify` 与 `Encrypt`、`Decrypt`）。接口按钉钉的公开文档建模（截至 2026-11），接真实钉钉前要复核（设计的未决问题）。
- 不负责：什么时候发、发给谁（`deliveries`）；回调的业务处理（`callbacks`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `client.go` | `Client`、`accessToken`、`SendCard`、`UserOfCode`、`ConsentURL` |
| `crypto.go` | `Sign`、`Verify`、`Encrypt`、`Decrypt` |
| `crypto_test.go` | 单元测试：加解密与签名 |
| `client_test.go` | 单元测试：应用的令牌被作废时换取用户重取令牌 |

## 依赖与被依赖

- 依赖：`core`；SDK 的 `calls`（出站调用、`ErrNotSent`、`StatusError`）。
- 被依赖：`callbacks`（加解密）、入口（建客户端）。

## 测试

`crypto_test.go`、`client_test.go`；`Client` 的其余部分由格里的 L3 对模拟钉钉测到（集成测试换掉了它）。
