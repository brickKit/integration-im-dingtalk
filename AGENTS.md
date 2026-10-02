[English](AGENTS.md) · [中文](AGENTS.zh.md)

# integration/im-dingtalk

The AI guide to developing this component. How to use it, its boundaries and contracts: BRICKKIT.md. Why it is shaped this way: `docs/design.md`. Dependencies, configuration and deployment: component.yaml.

## Code map

| Path | Owns |
|---|---|
| `backend/module/module.go` | The only entry, `New(ctx, rt)`: reads the DingTalk configuration (`readDingtalkConfig`, an error naming every bad key), builds repo → tokenmgr → service → HTTP + gRPC, starts three loops: the outbox pump, partition maintenance and the dispatch consumer |
| `backend/cmd/server/main.go` | One line, `besdk.RunStandalone(module.New)` |
| `backend/cmd/migrate/main.go` | One line, `migrate.Main(migrations.FS)`: the migration container's entry (`./migrate up`) |
| `backend/internal/consumer/consumer.go` | The only way a message gets sent: consumes the dispatch event, filters on `target_adapters`, resolves the userid, sends, records `ACCEPTED`, polls `getsendresult` in a goroutine, records `CONFIRMED`; `recordResultTx` writes the attempt and the result event in one transaction |
| `backend/internal/dingtalk/client.go` | The four DingTalk HTTP calls (`gettoken`, `getbymobile`, `asyncsend_v2`, `getsendresult`), 10-second timeout, transport errors stripped of the query string (`withoutQuery`) |
| `backend/internal/dingtalk/classify.go` | `ClassifyError` (retryable or not, evict the phone cache or not) and `IsTokenExpired` |
| `backend/internal/tokenmgr/tokenmgr.go` | `access_token` cache and refresh: `EnsureValid` (single flight under a mutex) and `Refresh` (unconditional) |
| `backend/internal/repo/` | `token.go` (the one-row `dingtalk_token`), `usermap.go` (phone → userid cache), `deliveries.go` (`delivery_attempts`, `BatchGetLatestStatus`, `ListDeliveries`) |
| `backend/internal/service/` | Read and admin operations shared by REST and gRPC; the error → gRPC status mapping (`status.go`) |
| `backend/internal/http/http.go` | The two admin routes under `/integration/im`, each registered with `integration.im.admin` |
| `backend/internal/grpc/grpc.go` | `integration.im.v1.ImChannelService` |
| `backend/internal/partition/` | One loop for all partitioned tables: weekly `event_outbox` / `event_inbox` four weeks ahead (`partition.go`), monthly `delivery_attempts` three months ahead (`monthly.go`) |
| `backend/internal/testlock/` | Test-only: `TokenRow` serialises, across test packages, the tests that touch the one-row `dingtalk_token` |
| `migrations/` | SQL migrations, embedded by `migrations/embed.go` |
| `contracts/` | proto, OpenAPI, event schema |
| `gen/integration/im/` | Generated Go code: a nested Go module, tagged on its own as gen/integration/im/v1.x.y; never edited by hand |

| Feature | Start here | Then |
|---|---|---|
| How a dispatch becomes a DingTalk message | `backend/internal/consumer/consumer.go` (`handle`) | `backend/internal/consumer/consumer_test.go` (fake DingTalk + real PostgreSQL and NATS) |
| A DingTalk error classified wrongly | `backend/internal/dingtalk/classify.go` | `backend/internal/dingtalk/classify_test.go` (real response texts) |
| A DingTalk response parsed wrongly | `backend/internal/dingtalk/client.go` | `backend/internal/dingtalk/client_test.go` |
| Token refreshed too often or not at all | `backend/internal/tokenmgr/tokenmgr.go` | `backend/internal/tokenmgr/tokenmgr_test.go` |
| A new partitioned table | `migrations/` (a new migration) | `backend/internal/partition/` (add it to the table list), `backend/internal/partition/partition_test.go` |
| A new admin query parameter | `backend/internal/http/http.go` | `backend/internal/repo/deliveries.go`, `contracts/im-dingtalk.openapi.yaml` |

## Build and test

```bash
# tests run against the test database brickkit_test_db, never brickkit_db (make test-db-init at the project root)
export TEST_PG_DSN="postgres://<user>:<password>@localhost:5432/brickkit_test_db?sslmode=disable"
export TEST_NATS_URL=nats://localhost:4222
make test                    # every package ends in "ok"; refuses to run without TEST_PG_DSN
go test ./... -count=1 -v | grep -c -- '--- SKIP'   # 0: no test skipped
make check-version dag-check contract-check import-scan module-check   # each prints one ✓ line
make docs-check              # "0 with errors, 0 warnings"
# migrations, as the login role:
PG_HOST=localhost PG_PORT=5432 PG_DATABASE=brickkit_test_db PG_USER=integration_im_dingtalk_rw \
  PG_PASSWORD=<its password> PG_SCHEMA=integration_im_dingtalk make migrate-idempotent   # ✓ 迁移幂等
```

The four DingTalk endpoints are faked with `httptest`; no real credentials are needed. The consumer tests publish to a test-private subject (`testSubject`), so a running container of this component never sees them. On a real machine, from the project root: `make verify ID=integration/im-dingtalk ROUTE=/integration/im/admin/deliveries FOCUS=1` builds the image, starts only what this component needs, checks migration, health and the permission check, runs a focus run, and tears down.

## Design decisions

- **Events only, no dependencies, no send rpc**: a `channel:*` member must stay optional; `make dag-check` fails on any dependency.
- **Two phases per delivery**: `asyncsend_v2` succeeding only means DingTalk accepted the task; `ACCEPTED` is recorded and published, the real outcome comes from polling `getsendresult` and is recorded as `CONFIRMED`.
- **The adapter classifies, the notification centre retries**: this component reports `retryable`, never retries a dispatch itself; the only thing it retries is one token refresh.
- **`EnsureValid` and `Refresh` differ on purpose**: only `EnsureValid` re-checks the cache after taking the lock.
- **The phone cache is evicted on "user does not exist"**, never refreshed on a timer (getbymobile is rate-limited).
- Reasons and the full contract surface: `docs/design.md`.

## Pitfalls

| Never | Symptom | Why |
|---|---|---|
| Handle a dispatch without checking `target_adapters` for `dingtalk` | A project with three IM channels delivers every notification three times | Every family member subscribes to the same subject and core NATS delivers to all |
| Let `Refresh` skip the call when the cached token is still valid | `POST /admin/token/refresh` answers `200` and nothing changed | Forced refresh must be unconditional; the re-check belongs to `EnsureValid` only |
| Publish `integration.im.result.v1` with a version that does not grow across phases and retries | The notification centre silently drops the later results as duplicates | Its inbox accepts only a strictly greater version per `record_id`; keep `attempt × 10 + phase` in `recordResultTx` |
| Poll `getsendresult` inside the consume transaction | Throughput collapses: every message holds a connection and its inbox claim for seconds | Polling runs in `confirmLater`, a goroutine outside the dispatch transaction, writing in its own transaction |
| Decide "done" from a progress field of `getsendresult` | Delivered messages stay `ACCEPTED` until the polls run out | The real response has no progress field, only recipient lists |
| Put the request URL or the raw `*url.Error` into an error, a log or `last_error` | When DingTalk is unreachable the AppSecret (gettoken's query) or the access_token appears in logs and in the `GetChannelHealth` response | `withoutQuery` keeps method and path only |
| Publish test events to `infra.notification.dispatch.im.v1` | A running container of this component handles them and calls the real DingTalk API | Use `testSubject`; only the production container listens on the production subject |
| Add a partitioned table without adding it to `backend/internal/partition/` | Everything passes until the first insert after the migration's partitions run out: "no partition of relation found for row", every dispatch fails | The migration creates only the first partitions; the loop creates the rest |
| Write a test that touches `dingtalk_token` without `testlock.TokenRow` | Tests fail at random under `go test ./...` | The table has one row; test packages run in parallel |
| `panic` or `MustString` in `New` for a bad configuration value | Inside a shell the whole shell crashes with a stack trace instead of failing with the member and key named | Return an error; `readDingtalkConfig` collects every bad key |
| Expose `dingtalk_user_map` through an API or an event | Phone numbers leave the component | The cache holds plain phone numbers, needed to call DingTalk; they stay inside |

## Before changing code

1. Is it a business decision (whether to notify, whom, how often to retry)? It belongs to `infra/notification`, not here.
2. Am I adding a dependency, a send rpc or a synchronous call into this component? Stop: the family must stay optional.
3. Contract change? It is the family's contract: append only, and ask whether every IM channel could implement it. Never remove or retype a field, rpc, path or subject.
4. Did `gen/` change? Then the contract package needs a new tag and `go.mod` must require it.
5. Touching DingTalk response parsing or error classification? Say whether the branch was checked against a real response (the comments mark it) and keep the fake servers in the tests in the real shape.
6. New rule → write the failing test first (real database for anything SQL), then the code.
7. Bump `metadata.version` before the first change after a release; update BRICKKIT.md and `docs/design.md` in the same commit as the code.

<!-- brickkit:managed:begin lang=en -->
<!-- maintained by brickkit (init, add, remove, upgrade, skills update): edits between these markers are overwritten -->

## BrickKit

This is a BrickKit component: `component.yaml` is all the platform reads. The rules it relies on:

- `configSchema` keys are the environment variable names the code reads. Never use a reserved name: `COMPONENT_ID`, `COMPONENT_VERSION`, `PORT`, `BRICKKIT_SERVED_MEMBERS`, `BRICKKIT_SERVED_MEMBERS_CONFIG`, or any `*_ENDPOINT`.
- Dependencies are exact versions. A dependency's address arrives as `<ID>_ENDPOINT`; an optional dependency that is absent has no variable at all, so read it with a fallback.
- `/healthz` checks only this process, never a dependency. The migration command runs from the same image and must fail on an argument it does not know.
- `BRICKKIT.md` travels to every project that uses this component and is read there without the repository: keep it in step with the code, with no relative links.
- Release: raise `metadata.version`, commit, push, `brickkit release`. `brickkit lint` checks the manifest and these docs — inside a project, run in this directory, it checks only this component (`--all` for the whole project).
- The full rules are in the `brickkit-component` skill (`.claude/skills/brickkit-component/SKILL.md` at the root of the project or repository where skills are installed; `brickkit skills update` installs it); for flags ask `brickkit <command> --help`; BrickKit's own documentation is `brickkit docs`.
<!-- brickkit:managed:end -->
