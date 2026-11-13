# core：共用的名字与工具

## 负责与不负责

- 负责：组件身份与流、配置键、资源与操作（`Definitions()`）、`Config` 与 `Base`、钉钉的接口 `DingTalk`（`Card`、`Button`、发送结果的分类）、随机值与摘要（`NewSecret`、`Digest`）、用户面的校验（`Check`、`Invalid`、`NotFound`）。
- 不负责：业务。

## 文件

| 文件 | 内容 |
| --- | --- |
| `names.go` | `ID`、`Stream`、配置键、资源与操作、`SendCard`、`Definitions()` |
| `base.go` | `Config`、`Base`、`NewSecret`、`Digest` |
| `dingtalk.go` | `Button`、`Card`、发送结果的分类、`DingTalk` 接口 |
| `rules.go` | `Check`、`Invalid`、`NotFound` |

## 依赖与被依赖

- 依赖：SDK。
- 被依赖：全部模块。

## 测试

由各模块的测试覆盖。
