[English](README.md) · [中文](README.zh.md)

# integration/im-dingtalk

IM 通道族里的钉钉成员：为 IM 派发事件发送钉钉工作通知，并发布投递结果。

## 在项目里使用

```bash
brickkit add integration/im-dingtalk@2.0.0
brickkit up
```

先看 BRICKKIT.zh.md 的"部署前准备"（PostgreSQL 的 schema 与登录角色、NATS、一个钉钉企业内部应用及到它的出网 HTTPS、授权与 JWKS 地址）。

## 文档

| 想知道 | 读 |
|---|---|
| 它做什么、不做什么，怎么配置，部署前要准备什么 | [BRICKKIT.zh.md](BRICKKIT.zh.md) |
| 它的 gRPC、REST 与事件契约 | [contracts/](contracts/) |
| 为什么这样设计：边界、投递的两个阶段、重试分工、未决问题 | [docs/design.zh.md](docs/design.zh.md) |
| 依赖什么（没有）、有哪些配置键 | [component.yaml](component.yaml) |
| 怎么开发：代码地图、测试、易错点 | [AGENTS.zh.md](AGENTS.zh.md) |

## 开发

Go 1.25、Gin、`database/sql` + pgx、经 be-sdk-go 的 golang-migrate。测试要真实的 PostgreSQL（`TEST_PG_DSN`）与 NATS（`TEST_NATS_URL`）；钉钉是模拟的，不需要凭据。命令与成功的样子见 AGENTS.zh.md 的"构建与测试"。在 BrickEnterprise 项目里，项目根目录跑 `make verify ID=integration/im-dingtalk FOCUS=1`：以容器和本机进程两种形态真机跑一遍，结束后收尾。
