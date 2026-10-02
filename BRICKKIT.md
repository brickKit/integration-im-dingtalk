# integration/im-dingtalk

## Purpose

The DingTalk member of the `channel:im` family: it turns the notification centre's IM dispatch events into DingTalk work notifications and reports back what happened. It holds no business logic: it sends what it is told, to whom it is told, and says whether a failure is worth retrying; whether to notify, whom, and how often to retry belong to the notification centre.

**Owns**

- Consuming `infra.notification.dispatch.im.v1` and keeping only the events whose `target_adapters` contains `dingtalk`.
- Translating a phone number into a DingTalk `userid` (`topapi/v2/user/getbymobile`), cached in `dingtalk_user_map`; the cached row is deleted when DingTalk says the user does not exist.
- The DingTalk app credentials and the `access_token`: cached in the database, refreshed five minutes before it expires, refreshed once more by itself when DingTalk reports it expired.
- Sending the text work notification (`asyncsend_v2`) and confirming it (`getsendresult`): every delivery has an `ACCEPTED` and a `CONFIRMED` phase, each recorded in `delivery_attempts` and published as `integration.im.result.v1`.
- Classifying a failure as retryable or not (DingTalk's error codes are DingTalk's).

**Does not own**

- Whether a notification is sent, to whom, its text, user preferences, the retry policy (how often, how many times): `infra/notification`.
- Users and their phone numbers: `infra/iam-casdoor` holds them; the notification centre puts the phone number into the dispatch event, so this component never looks a user up.
- Other IM channels (WeCom, Feishu, Slack, Teams): other members of the same family, sharing this component's contracts.

## Before you deploy

- **PostgreSQL**: a schema `integration_im_dingtalk`, plus `integration_im_dingtalk_archive` if your schema convention creates one (nothing writes to it yet). A login role `integration_im_dingtalk_rw` with `USAGE` and `CREATE` on `integration_im_dingtalk`, and its password. The migration runs as this role and creates the tables, so the role owns them; the running component creates the future partitions of `event_outbox` / `event_inbox` (weekly) and `delivery_attempts` (monthly) itself, which needs that ownership. BrickKit creates none of this; in the BrickEnterprise assembly project `make dev-env` writes the password into `.env` and `make db-init` creates the schemas, role and grants.
- **NATS** reachable at `NATS_URL`: dispatch events arrive over a core NATS subscription (a message published while the component is down is not delivered later); results leave through an outbox table and a background pump, so they wait in the outbox while NATS is unreachable.
- **A DingTalk internal app** ("企业内部应用") with its AppKey, AppSecret and AgentId, granted the permission to look up users by phone number (`qyapi_get_member_by_mobile`) and to send work notifications. Without the first permission every delivery to a phone number not cached yet fails, not retryable, with `DINGTALK_88`.
- **Outbound HTTPS** from where the component runs to `DINGTALK_BASE_URL` (by default `https://oapi.dingtalk.com`). A network with no way out should not install this component (`channel:im` members are optional) rather than install it and fail every delivery.
- Without real DingTalk values (a local or test environment) the component starts and runs normally; every delivery fails when it asks DingTalk for a token, is recorded as a `CONFIRMED` failure and published as such. Nothing crashes; this component retries nothing (the notification centre decides from `retryable`).
- **Authorization** (`infra/authz`) and **identity** (`infra/iam-casdoor`, or any IAM serving a JWKS) reachable at `AUTHZ_BUNDLE_URL` and `IAM_JWKS_URL` for the two admin REST routes to answer anything but errors. They are configuration, not dependencies: the component starts without them.

## Dependencies

None. The component lives on events only: it consumes the notification centre's dispatch event and publishes a result event back, so it never calls the notification centre, and nothing calls it to send a message (there is no send rpc). A dependency edge to a `channel:*` member would make that channel mandatory; with none, which IM channels a project installs is decided in `brickkit.yaml` alone. DingTalk's open platform is an external service reached through `DINGTALK_BASE_URL`, not a component.

The authorization bundle and the JWKS are fetched from the URLs in the configuration, not through a dependency edge. Without them every protected route fails closed: no or invalid token → `401`; `IAM_JWKS_URL` empty or unreachable → `403`; the bundle never fetched yet → `503`; a valid user without the permission key → `403`. `/healthz` stays `200` throughout: it reports only that this process is alive, never whether DingTalk is reachable.

## Configuration

| Variable | Meaning |
|---|---|
| `PG_HOST` | PostgreSQL host. Usually the project's shared value (`$var:PG_HOST`). |
| `PG_PORT` | PostgreSQL port; default `5432`. |
| `PG_DATABASE` | The database holding the `integration_im_dingtalk` schema (`$var:PG_DATABASE`). |
| `PG_USER` | The login role, `integration_im_dingtalk_rw` as a literal. Inside a shell the shell logs in with its own role and switches to this one per transaction (`SET LOCAL ROLE`), so the role name must be `<PG_SCHEMA>_rw`. |
| `PG_PASSWORD` | Password of `PG_USER`. Secret: write `${INTEGRATION_IM_DINGTALK_DB_PASSWORD}` (or your secret store's reference), never the value. |
| `PG_SCHEMA` | Schema of all tables, the outbox and the migration state table (`schema_migrations_integration_im_dingtalk`); default `integration_im_dingtalk`. Write the literal anyway so every component's schema is visible in one place. |
| `NATS_URL` | NATS server: dispatch events come in here, the outbox pump publishes results here (`$var:NATS_URL`). |
| `OTEL_BASE_URL` | OpenTelemetry collector base URL; empty (the default) exports nothing. |
| `AUTHZ_BUNDLE_URL` | URL of the authorization bundle the permission check polls, for example `http://infra-authz-2-0-0:8223/authz/bundle`. Required: without it the admin routes answer `503`. Keep it in step with the authz version the project runs. |
| `IAM_JWKS_URL` | URL of the JWKS used to verify user tokens locally, for example `http://infra-iam-casdoor-2-0-0:8200/.well-known/jwks.json`. Required: without it the admin routes answer `403`. |
| `DINGTALK_BASE_URL` | Base URL of the DingTalk open platform; default `https://oapi.dingtalk.com`. Change it only for a corporate proxy or a test double; leave it unset otherwise. |
| `DINGTALK_APP_KEY` | AppKey of the DingTalk internal app. An identifier, not a secret; still usually written as `${…}` so the three DingTalk values live together in the secret store. |
| `DINGTALK_APP_SECRET` | AppSecret of the same app. Secret: write `${INTEGRATION_IM_DINGTALK_DINGTALK_APP_SECRET}` (or your secret store's reference), never the value. |
| `DINGTALK_AGENT_ID` | AgentId of the same app, an integer; work notifications are sent in its name. A value that is not an integer stops the component at start with an error naming the key. |

## Contracts

- `contracts/integration/im/v1/im.proto` — gRPC `integration.im.v1.ImChannelService`, the family-level contract every `channel:im` member implements: `BatchGetDeliveryStatus` (the current status of each `record_id`, `CONFIRMED` preferred over `ACCEPTED`, in the order asked, missing IDs left out) and `GetChannelHealth` (whether a valid token is at hand — cached, or fetched from DingTalk now — when it expires, the last error; for operators, not for business decisions). There is no rpc that sends a message. The Go package is the separate module `github.com/brickKit/integration-im-dingtalk/gen/integration/im`.
- `contracts/im-dingtalk.openapi.yaml` — REST under the family prefix `/integration/im` (not `/integration/im-dingtalk`), both routes behind `integration.im.admin`: `GET /admin/deliveries` (the delivery attempts, filtered by `record_id` and `phase`, paged by `cursor` / `page_size`: 1–200, anything else means 50) and `POST /admin/token/refresh` (force a new `access_token` now, unconditionally).
- `contracts/events/im-dingtalk.events.json` — published through the outbox: `integration.im.result.v1`, once per phase (`ACCEPTED` when DingTalk accepted the task, `CONFIRMED` with the real outcome), carrying `record_id`, `adapter: dingtalk`, `phase`, `attempt`, `success`, `error_code`, `retryable`, `external_task_id`. Its version is `attempt × 10 + phase` (`ACCEPTED` = 1, `CONFIRMED` = 2), so it grows across phases and retries. Consumed: `infra.notification.dispatch.im.v1` (`record_id`, `attempt`, `target_adapters`, `recipient_phone`, `body`; `title` is not sent, a text work notification carries `body` only).
- `assembly.yaml` — this project's metadata: the permission key `integration.im.admin`, the menu entry, the edge route `/integration/im/**`, the schema and role, and `data_scopes: none`.

## Shell declaration

Not a shell. It can be hosted in a Go shell (in the BrickEnterprise project, `be/go-infra`) or run on its own; the code is the same either way.
