# deliveries：投递与发卡片

## 负责与不负责

- 负责：表 `delivery`；`Deliver`（ID01：先按“调用方 + 投递身份”幂等，内容不同答 `IDEMPOTENCY_KEY_REUSED`，已受理的过了到期也答第一次的结果；再查类别、标题、链接、到期、动作的形状（动作名按动作事件的 schema）；没有绑定答 `unmapped` 什么也不写；否则写投递与带放弃时刻的对外命令）；`Adapter`（ID03：每次发送重新生成每个动作的按钮随机值，只存摘要，卡片加“在系统中处理”的链接；不能查询）；`Capabilities`（ID11）；`Settled`（ID04：命令终结时记结果、发结果事实；放弃时刻到了记 `expired`）；`Purge`（到期 7 天后、且建立 90 天后删）。
- 不负责：钉钉的接口（`dingtalk`）；回调（`callbacks`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `deliveries.go` | `Action`、`Deliver`、`request`、`valid`、`Adapter`（`Send`、`Query`）、`Capabilities`、`Settled`、`Purge` |

## 依赖与被依赖

- 依赖：`core`、`bindings`；SDK 的 `channelpb`、`channel`、`extcmd`。
- 被依赖：`callbacks`（读动作）、入口。

## 测试

`itest/flows_integration_test.go` 的 `deliveries`、`sending`、`expiry`。
