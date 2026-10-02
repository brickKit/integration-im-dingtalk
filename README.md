[English](README.md) · [中文](README.zh.md)

# integration/im-dingtalk

The DingTalk member of the IM channel family; sends work notifications for IM dispatch events and publishes the delivery results.

## Use it in a project

```bash
brickkit add integration/im-dingtalk@2.0.0
brickkit up
```

Prepare first: see "Before you deploy" in BRICKKIT.md (a PostgreSQL schema and login role, NATS, a DingTalk internal app and outbound HTTPS to it, and the authorization and JWKS URLs).

## Documentation

| To find out | Read |
|---|---|
| What it does and does not do, how to configure it, what to prepare | [BRICKKIT.md](BRICKKIT.md) |
| Its gRPC, REST and event contracts | [contracts/](contracts/) |
| Why it is designed this way: boundary, the two delivery phases, retries, open questions | [docs/design.md](docs/design.md) |
| What it depends on (nothing) and its configuration keys | [component.yaml](component.yaml) |
| How to develop it: code map, tests, pitfalls | [AGENTS.md](AGENTS.md) |

## Development

Go 1.25, Gin, `database/sql` with pgx, golang-migrate through be-sdk-go. Tests need a real PostgreSQL (`TEST_PG_DSN`) and NATS (`TEST_NATS_URL`); DingTalk is faked, no credentials needed. The commands and what success looks like are in AGENTS.md, section "Build and test". Inside the BrickEnterprise project, `make verify ID=integration/im-dingtalk FOCUS=1` at the project root runs it for real, in a container and as a local process, and tears down afterwards.
