# integration/im-dingtalk

钉钉渠道成员的代码。用法与边界在 `BRICKKIT.md`，为什么这样设计（术语、数据、规则、未决问题）在 [docs/design.md](docs/design.md)；跨组件的规则在装配项目 be-assembly-standard 的协议 10（对外命令、入站回调、通知与交互按钮）与决定 D68；与通知组件、任务所属组件之间的约定在线协议第 20 章，对外命令在第 13 章。

## 代码地图

代码按业务模块分层。**先读这一节，再进目录**：每个代码目录都有自己的 `AGENTS.md`（同目录的 `CLAUDE.md` 让 Claude Code 读这个目录的文件时自动带上它）。

| 目录 | 是什么 |
| --- | --- |
| [`dingtalk/`](dingtalk/AGENTS.md) | 入口：组件定义与组装（用户面路由、系统面 `be.channel.v1.ChannelService`、对外命令的提供与终结钩子、作业）；不写业务 |
| [`internal/`](internal/AGENTS.md) | 业务：下面的模块各一个包；模块之间的依赖表在它的 `AGENTS.md` |
| [`itest/`](itest/AGENTS.md) | 集成测试（真实的 PostgreSQL 与消息系统，假的钉钉）；它的 `AGENTS.md` 是测试索引 |
| `dingtalk/migrations/` | 表：绑定、绑定会话、投递、回调 |
| `cmd/seed/` | 种子（`make seed`）：角色与授予（绑定自己的钉钉给要用的种子用户，维护给种子用户） |
| `contract/` | 本组件发布的契约：`contract/openapi.yaml`（用户面）、`contract/events/`（动作与投递结果，线协议的共享 schema 的副本）；系统面是线协议的 `be.channel.v1`（SDK 的 `channelpb`），本组件没有自己的 proto |
| `docs/design.md` | 设计（ID01–ID12） |

业务模块（`internal/` 下）：

| 模块 | 负责 | 拥有的表 |
| --- | --- | --- |
| [`bindings`](internal/bindings/AGENTS.md) | 本人绑定（OAuth 的会话、要登录的回跳、换取用户）、查看与解除、管理员的列表与结束；`UserOf`、`PrincipalOf`；清理 | `binding`、`bind_session` |
| [`deliveries`](internal/deliveries/AGENTS.md) | `Deliver`（先幂等后校验、没有绑定答 `unmapped`、写投递与对外命令）、对外命令的 `Adapter`（每次发送重新生成按钮随机值，只存摘要）、终结钩子（结果事实）；清理 | `delivery` |
| [`callbacks`](internal/callbacks/AGENTS.md) | 回调的接收（验签、时间窗、解密、去重、存 `received` 并登记工作项、答成功）、处理（发动作事件或隔离）、隔离区、卡住的告警；清理 | `callback` |
| [`dingtalk`](internal/dingtalk/AGENTS.md) | 钉钉的接口（访问令牌、互动卡片与纯文本转义、OAuth）与回调的签名与加解密 | — |

共用的：[`core`](internal/core/AGENTS.md)（名字、资源与操作、配置键、`Base`、钉钉的接口 `DingTalk`、随机值与摘要、校验）。

| 想改 | 从这里开始 | 然后 |
| --- | --- | --- |
| 投递怎样交来、幂等、没有账号 | ID01；线协议 P20.1 | `internal/deliveries` 的 `Deliver` |
| 发送的结果怎样分类、什么时候不再发 | ID03、ID11；线协议 P13.5、P13.14 | `internal/dingtalk` 的 `SendCard`、`internal/deliveries` 的 `Adapter`、`Settled` |
| 回调怎样验证与处理 | ID06–ID08；线协议 P20.6 | `internal/callbacks` 的 `Receive`、`Process`、`judge`；`internal/dingtalk` 的 `crypto.go` |
| 谁能绑定、管理员能做什么 | ID10；线协议 P20.9 | `internal/bindings` |

## 构建与测试

- `go test ./...`（`make test`）：清单的 `configSchema` 与 `events`、OpenAPI 与路由、合并安全（`dingtalk/contract_test.go`）；回调的签名与加解密（`internal/dingtalk`）。
- `make test-integration`（`go test -tags integration ./itest/`）：要 `BE_SDK_TEST_PG_ADMIN`、`BE_SDK_TEST_NATS_URL`（装配项目的 `itenv.sh`）。
- `brickkit lint`

## 设计取舍

- **秘密只在本组件与钉钉之间**：按钮随机值与绑定的 `state` 只存摘要；组件之间流动的只有不是秘密的动作授权（线协议第 20 章）。
- **回调先落库再答成功**：存 `received` 与登记工作项同一个事务，处理以回调行幂等。
- **结果未知不重发**：钉钉不承诺去重、不能查询，读取超时的命令等到放弃时刻转 `expired`，不进人工。
- **不与不受信任的成员同壳**：同一个外壳的成员共用总线连接，能在本组件的流上发布动作事件。

## 易错点

- 不要把按钮随机值或 `state` 写进日志、事件或表（只存摘要）；`SendCard` 的答复只写短的分类。
- 不要信回调里的用户就是收件人：映射出的主体要等于投递的收件主体（转发的卡片进隔离区）。
- 不要加让管理员设置绑定的路由：能设绑定就能以别人的身份点按钮。
- 不要把绑定的回跳改成公开：只有登录的发起人能完成绑定（别人转来的授权链接绑不上）。
- 不要把凭据放进钉钉接口的地址：新接口的令牌与密钥都在请求体或请求头里；旧接口地址里的短期令牌靠 SDK 不把查询串放进错误。

## 改代码前自查

- 改了版本号吗？每次改动都升版本。

## BrickKit

这是一个 BrickKit 组件：平台只读 `component.yaml`。它依赖的规则：

- `configSchema` 的键就是代码读的环境变量名。不能用保留名：`COMPONENT_ID`、`COMPONENT_VERSION`、`PORT`、`BRICKKIT_SERVED_MEMBERS`、`BRICKKIT_SERVED_MEMBERS_CONFIG`，以及任何 `*_ENDPOINT`。
- 依赖写精确版本。依赖的地址以 `<ID>_ENDPOINT` 注入；缺席的可选依赖根本没有这个变量，读的时候要带兜底。
- `/healthz` 只查本进程，不查依赖。迁移命令用同一个镜像跑，遇到不认识的参数必须直接失败；`local.migrationCommand` 从源码跑同一条迁移，给 `mode: local` 用。
- `BRICKKIT.md` 会随版本进入每个使用它的项目，在那里是脱离仓库单独读的：跟代码一起改，不放相对链接。
- 发版：改 `metadata.version`，提交、推送，`brickkit release`。`brickkit lint` 会检查清单和这些文档——在项目里、在这个目录下跑，只查这个组件（`--all` 查整个项目）。
- 完整规则在 `brickkit-component` 技能里（装了技能的项目或仓库根目录下的 `.claude/skills/brickkit-component/SKILL.md`；`brickkit skills update` 会装上）；参数问 `brickkit <命令> --help`；BrickKit 自己的文档用 `brickkit docs` 看。
