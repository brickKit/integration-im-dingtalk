# bindings：本人绑定

## 负责与不负责

- 负责：表 `binding`、`bind_session`；`Start`（开绑定会话，答钉钉的授权页）、`Callback`（公开的回跳：`state` 原子地用一次、在事务外换取用户、建立绑定；这个钉钉用户已绑给别人答 `taken`；跳回前端）、`Mine`、`UnbindMine`、`List`、`End`（管理员只能结束）；`UserOf`、`PrincipalOf`（投递与回调用）；每次建立与结束写审计（ID10，线协议 P20.9）。
- 不负责：管理员设置绑定（不存在这条路：能设绑定就能以别人的身份点按钮）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `bindings.go` | `SessionLife`、`UserOf`、`PrincipalOf`、`Start`、`Callback`、`bind`、`Mine`、`UnbindMine`、`End`、`List` |

## 依赖与被依赖

- 依赖：`core`。
- 被依赖：`deliveries`、`callbacks`、入口。

## 测试

`itest/flows_integration_test.go` 的 `bindings`。
