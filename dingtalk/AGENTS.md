# dingtalk/：入口与组装

组件对外的一个包：`main.go` 与外壳只引用它（`dingtalk.Definition()`、`dingtalk.New`）。这里只**组装**；业务一行也不写。

## 文件

| 文件 | 内容 |
| --- | --- |
| `definition.go` | `Version`、端口、迁移、`Keys` 与 `Own`（钉钉的地址、应用、卡片模板、回调的密钥、前端与回跳地址）；`Definition()`（流 `integration.im_dingtalk`、对外命令、补发的操作） |
| `module.go` | `Module` 与 `New`（配置、经 SDK 出站调用的钉钉客户端、对外命令的终结钩子 `deliveries.Settled`）；`UseClient`（集成测试换钉钉）；`ExternalCommands`（发卡片的适配器与能力）；`Routes`（两条公开路由：回调与绑定的回跳；本人绑定三条；管理员两条；隔离区）；`Services`（`ChannelService`，只许 `infra/notification`）；`Deliver`；`Jobs`（回调的处理、卡住的告警） |
| `checks.go` | `RecoveryChecks`：没有自己的检查 |
| `contract_test.go` | 单元测试：清单的 `configSchema` 与 `events`、OpenAPI 与路由一一对应、合并安全 |

## 依赖与被依赖

- 依赖：`internal/` 下全部模块；SDK 的 `channelpb`、`extcmd`。
- 被依赖：`main.go`、外壳、`itest/`。

## 测试

`go test ./dingtalk/`。
