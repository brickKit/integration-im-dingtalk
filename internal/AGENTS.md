# internal/：业务模块

钉钉渠道成员的业务，每个模块一个包。只给本仓库引用（Go 的 `internal`，决定 0101）。

## 模块

| 包 | 一句话 | 依赖（本组件内） |
| --- | --- | --- |
| `callbacks` | 回调的接收、处理（动作事件或隔离）、隔离区 | `core`、`bindings`、`deliveries`、`dingtalk` |
| `deliveries` | 投递、发卡片的适配器、结果事实 | `core`、`bindings` |
| `bindings` | 本人绑定与管理员的结束 | `core` |
| `dingtalk` | 钉钉的接口与回调的加解密 | `core` |
| `core`（共用） | 名字、资源与操作、配置键、`Base`、`DingTalk` 接口、随机值与摘要、校验 | — |

分层（每个包只引用比它低的层，没有环）：

| 层 | 包 |
| --- | --- |
| 4 | `callbacks` |
| 3 | `deliveries` |
| 2 | `bindings`、`dingtalk` |
| 1 | `core` |

核对：`go list -f '{{.Imports}}' ./internal/<包>`。

## 共同的写法

- **每个模块的函数先收 `m *core.Base`**（运行时、配置、钉钉客户端）。
- **秘密只存摘要**（`core.NewSecret`、`core.Digest`）。
- **对钉钉的调用在事务之外**；对外命令由 SDK 的 `extcmd` 发送。

## 测试

`dingtalk` 有单元测试（加解密）；其余由 `itest/` 的流程测试覆盖。
