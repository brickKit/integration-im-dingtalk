[English](design.md) · [中文](design.zh.md)

# integration/im-dingtalk design

Conclusions only, for whoever changes this component's design. How to use it: `BRICKKIT.md`; how the code is laid out: `AGENTS.md`.

## Boundaries

An adapter in the Channel Adapter sense: it listens to an event, calls an external API and publishes a result event, and holds no business logic. It is the first member of the `channel:im` family (DingTalk, WeCom, Feishu, Slack, Teams), and the family's contracts are shaped by it: whatever it looks like, the other members must look like.

| Not here | Owner | Why |
|---|---|---|
| Whether to send a notification, to whom, user preferences | `infra/notification` | An adapter sends what it receives; deciding is business logic |
| The text of the message | `infra/notification` | The dispatch event carries rendered text |
| The retry policy (how long to wait, how many times) | `infra/notification` | Retrying in both places multiplies the attempts (three outside × three inside = nine messages) |
| Users and their phone numbers | `infra/iam-casdoor` | The notification centre puts the phone number into the dispatch event; looking it up here would add a dependency on the IAM slot |
| Email, SMS, in-app messages | their own channels | This component is DingTalk only |

What is here and nowhere else: translating a phone number into a DingTalk `userid`, the DingTalk credentials and `access_token`, and judging whether a DingTalk failure is worth retrying. The phone → userid translation lives in the adapter because it is DingTalk-specific: every IM platform identifies a recipient differently (DingTalk a userid, Slack a member ID, Feishu an open_id), so the family contract carries only a stable natural identifier (the phone number) and each adapter translates it.

## Owned data

| Table | Partitioned | Notes |
|---|---|---|
| `dingtalk_user_map` | no | phone (primary key) → `userid`, `resolved_at`. A cache: getbymobile is rate-limited, so not every message may look the user up. Holds plain phone numbers (needed to call DingTalk, cannot be hashed): no API reads it and no event carries it |
| `dingtalk_token` | no | one row (`id = 1`, `CHECK`): `access_token`, `expires_at`. In the database, not only in memory, because requesting a new token on every restart runs into gettoken's rate limit |
| `delivery_attempts` | monthly, by `attempted_at` | one row per phase per attempt, written once and never updated: `record_id`, `phase` (`ACCEPTED` / `CONFIRMED`), `success`, `error_code`, `retryable`, `external_task_id` (DingTalk's `task_id`, the key for polling the result). Indexed by `(record_id, attempted_at DESC)` |
| `event_outbox`, `event_inbox` | weekly, by `created_at` | the standard pair: results leave through the outbox; the inbox makes consuming a dispatch idempotent |

There is no `command_idempotency` table: it serves write rpcs that carry a caller's `idempotency_key`, and this component has no write rpc. The only write path is consuming a dispatch event, made idempotent by `event_inbox`.

The phone cache expires by eviction, not by time: when DingTalk says the user does not exist or is not in the organisation, the row is deleted and the next delivery looks the user up again (people leave, change numbers). A timed full refresh would eat the rate limit.

## Contract surface

gRPC `integration.im.v1.ImChannelService` — the package is the family's `integration.im`, not `integration.im.dingtalk`: every member implements the same proto, and a caller does not change imports when a project switches channels.

| rpc | Kind | Notes |
|---|---|---|
| `BatchGetDeliveryStatus` | read | the batched read for troubleshooting: the current status per `record_id` (`CONFIRMED` before `ACCEPTED`, then the latest), in the order asked, missing IDs left out |
| `GetChannelHealth` | read | whether a valid token is at hand and when it expires, the last error. For operators, not for business decisions; it is the only place that probes DingTalk, and it is not `/healthz` |

There is deliberately no rpc that sends a message. With one, the notification centre or a business component could call this component synchronously, and a dependency edge to a `channel:*` member makes that channel mandatory. Messages come in only as events.

REST under the family prefix `/integration/im` (not `/integration/im-dingtalk`), both behind `integration.im.admin`:

| Path | Notes |
|---|---|
| `GET /admin/deliveries` | troubleshooting: what was sent for a notification and what DingTalk answered; filters `record_id`, `phase`; paged by an ID cursor |
| `POST /admin/token/refresh` | emergency: request a new `access_token` now, whatever the cache says |

No webhook endpoint: DingTalk work notifications are one-way and need no callback address.

## Events

Consumed:

| Subject | From | What happens |
|---|---|---|
| `infra.notification.dispatch.im.v1` | `infra/notification` | First: does `target_adapters` contain `dingtalk`? If not, drop it — every family member subscribes to this one subject and core NATS delivers to all, so skipping the check delivers every notification once per installed channel. If so: resolve the userid, send, record and publish `ACCEPTED`, poll for the outcome, record and publish `CONFIRMED`. Idempotent per `(subject, record_id, version)` through `event_inbox` |

Published:

| Subject | When | Payload |
|---|---|---|
| `integration.im.result.v1` | once per phase | `record_id`, `adapter: dingtalk`, `phase`, `attempt`, `success`, `error_code`, `retryable`, `external_task_id` |

The subject is the family's, with the member in `adapter`: the notification centre consumes one subject for all IM channels, so adding a channel changes nothing there.

**Sending is asynchronous at DingTalk.** `asyncsend_v2` returns a `task_id`, which only means the task was accepted. Reporting success at that point would claim a delivery nobody can confirm, so every delivery has two phases:

1. `asyncsend_v2` succeeds → record and publish `ACCEPTED` (the notification centre shows "accepted", not "sent").
2. A few seconds later poll `getsendresult(task_id)`, doubling the wait each round, at most four rounds (5 s, 10 s, 20 s, 40 s) → record and publish `CONFIRMED` with the real outcome. The response has no progress field: the recipient appears in one of the lists `read_user_id_list` / `unread_user_id_list` (success), `failed_user_id_list` (failed, retryable), `invalid_user_id_list` / `forbidden_list` (failed, not retryable), or in none yet (still processing). When the rounds run out, `CONFIRMED` with `GET_SEND_RESULT_TIMEOUT`, retryable — better than staying `ACCEPTED` forever.

Polling runs in a goroutine with its own transaction, never inside the consume transaction (sleeping there would hold a connection and the inbox claim). Skipping phase 2 is not an option: "the user is not in the organisation", the most common failure, only shows up there, and the phone-cache eviction depends on it.

Failures before DingTalk accepted anything (no userid, no token, `asyncsend_v2` rejected) are recorded directly as `CONFIRMED` failures; there is no `ACCEPTED` phase for them.

**The version must grow.** All results of one `record_id`, across phases and retries, share `(subject, aggregate_id)`, and the notification centre's inbox accepts only a strictly greater version. Version = `attempt × 10 + phase` (`ACCEPTED` = 1, `CONFIRMED` = 2), with `attempt` taken from the dispatch payload.

**`retryable` is where adapter and notification centre divide the work.** Only the adapter can tell whether a DingTalk error is worth retrying; the notification centre decides whether and when to retry:

| DingTalk answer | `retryable` | Why |
|---|---|---|
| network error, "system busy", rate limiting | yes | temporary |
| user does not exist / not in the organisation | no | retrying changes nothing; the phone cache row is deleted |
| invalid AppKey / AppSecret | no | a configuration error; retrying only floods the log, operators must see it |
| app lacks a permission (errcode 88) | no | someone must grant it in the developer console |
| `access_token` expired | — | the adapter refreshes the token and retries once itself, without spending the notification centre's retries |
| anything not recognised | yes | the retry limit is the notification centre's |

Errors are matched by keywords in `errmsg`, not by `errcode`. Only the permission case has been seen in a real response; the other branches follow DingTalk's public documentation.

## Dependencies

None, and none expected:

| Not a dependency | Why |
|---|---|
| `infra/notification` | both directions are events; there is nothing to call synchronously |
| `infra/iam-casdoor` | the phone number arrives in the dispatch payload, put there by the notification centre; an edge to IAM would add a dependent to the IAM slot |
| `infra/authz` | the permission bundle is polled from `AUTHZ_BUNDLE_URL`: configuration, not an edge |
| DingTalk open platform | an external service, reached at `DINGTALK_BASE_URL` with the app credentials from the configuration |

## Place in the synchronous call graph

Not in it: no outgoing edges and, without a send rpc, no incoming ones either. It lives at the far end of the event graph, the last stop of the notification chain. It touches neither CRM nor ERP and none of the three hubs.

It is one of the few components that reaches the public internet. A customer's private, fully offline deployment should not install it (the family is optional); this is a question for assembly time, not something to discover when every delivery fails.

## Partitioning and archiving

| Data | Hot | Then |
|---|---|---|
| `delivery_attempts` | the last month | whole months older than that belong in `integration_im_dingtalk_archive` |
| `dingtalk_user_map` | always | never archived; stale rows are deleted (see Owned data) |
| `dingtalk_token` | one row | never archived |
| `event_outbox`, `event_inbox` | 30 days after publishing | deleted |

Moving and deleting old partitions is not implemented yet (see Open questions); the table above is the target. The archive window is the shortest in the system: delivery attempts are troubleshooting data, valuable only in the first days after a problem; the authoritative notification history is in `infra/notification`. Keeping both for long would duplicate it.

The migration creates only the first partitions; the partition loop in the running component creates the rest ahead of time (weekly four weeks ahead, monthly three months ahead), so a missing future partition never breaks an insert.

## Data scopes

None (`data_scopes: none`). The component has no user-facing data, only the delivery attempts operators read while troubleshooting, behind one admin permission.

## Reference implementations

| Project | Version | Module consulted | What was borrowed | License | Usage |
|---|---|---|---|---|---|
| DingTalk open platform | public documentation | work notifications (`asyncsend_v2`, `getsendresult`), user by phone, `gettoken` and its limits | the whole external interface; the request and response shapes were later checked against real responses | proprietary platform | Borrowed in practice |
| Novu | — | provider interface (send input and output, error classification) | which failures are permanent: the reasoning behind the `retryable` table | MIT | Borrowed reasoning |
| Apache Camel | — | Enterprise Integration Patterns: Message Translator, Channel Adapter | the role itself, and that an adapter carries no business logic | Apache-2.0 | Borrowed in practice |

**Deliberately avoided**:

| Common practice | Why not here |
|---|---|
| Retry and back-off inside the adapter | Retrying belongs to the notification centre; doing it in both places multiplies attempts |
| One subject per channel, the notification centre subscribing to each | Adding a channel would change the notification centre; one family subject plus `target_adapters` does not |
| The adapter looks up the recipient in the user centre | It would add a dependent to the IAM slot; the recipient arrives in the dispatch event |

The WeCom, Feishu and Slack SDKs were deliberately not read yet: designing the family contract from them now would either make it the union of three platforms or freeze it to DingTalk. The contract is the smallest common case, "send one text to one natural identifier", to be re-checked when the second member is built.

The family itself is the slot-family answer to "several implementations are right for different customers" (DingTalk or WeCom in China, Slack or Teams abroad). Inside this component there is no further variant.

## Open questions

| Question | Current answer |
|---|---|
| A restart while `confirmLater` is polling leaves that delivery at `ACCEPTED` for good | Known gap: pending polls are not persisted. The notification centre is expected to surface records stuck at `ACCEPTED`; a startup scan for unconfirmed `ACCEPTED` rows is the fix if it matters in practice |
| A dispatch whose handling fails on the database (not on DingTalk) is lost | `besdk.Consume` uses a core NATS subscription and only logs a failed handler; no redelivery, no dead letter. At-least-once needs a JetStream consumer, an SDK-level decision |
| A message accepted by DingTalk whose `ACCEPTED` row then fails to commit | The transaction rolls back with the inbox claim, so no result is published and a retry by the notification centre sends the message again. The external call cannot be part of the transaction; accepted as rare |
| `confirmLater` starts before the dispatch transaction commits | If that commit fails, a `CONFIRMED` row can appear without its `ACCEPTED`; the first poll waits 5 s, which makes it unlikely |
| Archiving `delivery_attempts` after a month, deleting old outbox / inbox partitions | Not implemented yet: nothing moves or drops old partitions, they stay in the hot schema. The same holds across the project; an SDK-level partition retention helper is the likely answer |
| `GET /admin/deliveries` has no default time window, unlike the project's lists | It is an admin troubleshooting view over a table meant to stay one month hot; add `attempted_after` / `attempted_before` (append-only) if it grows |
| Interactive cards (approve in DingTalk) | Not done. It would need a webhook endpoint and would map DingTalk callbacks onto workflow actions — the first time this component would carry business meaning, so the boundary must be re-examined first |
| Does the family contract fit WeCom, Feishu, Slack? | Re-check when the second member is built; before adding a field, ask whether the other members can implement it |
