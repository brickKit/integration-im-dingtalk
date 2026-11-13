# callbacks：钉钉的回调

## 负责与不负责

- 负责：表 `callback`；`Receive`（ID06：公开路由，大小、形状、时间窗、签名、解密不过的丢弃并计数、不写库；按事件身份去重；随机值换成摘要后存 `received`，同一事务登记 `JobProcess`；提交后答加密的 `success`）；`Process` 与 `judge`（ID07、ID08：以回调行幂等；不是卡片回调的 `ignored`；找不到投递、没有绑定、不是收件人、随机值对不上的进隔离区；否则发动作事件，受众是投递的发送方）；`Quarantined`（ID09）；`Stuck`（停在 `received` 超过 5 分钟告警）；`Purge`（处理过的 180 天后删，停在 `received` 的不删）。
- 不负责：执行动作（所属组件）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `callbacks.go` | `JobProcess`、`Counters`、`Receive`、`answer`、`redact`、`Process`、`judge`、`Stuck`、`Quarantined`、`Purge` |

## 依赖与被依赖

- 依赖：`core`、`bindings`、`deliveries`、`dingtalk`；SDK 的 `channel`。
- 被依赖：入口。

## 测试

`itest/flows_integration_test.go` 的 `callbacks`。
