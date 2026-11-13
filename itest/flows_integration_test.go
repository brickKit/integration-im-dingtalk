//go:build integration

package itest

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/brickKit/integration-im-dingtalk/v3/dingtalk"
	core "github.com/brickKit/integration-im-dingtalk/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/channelpb"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// back is DingTalk's redirect back to the binding route in the user's signed-in browser; answers where the browser
// is sent.
func (fx *fixture) back(as client, code, state string) string {
	fx.t.Helper()
	status, location := fx.backAs(as.token, code, state)
	require.Equal(fx.t, 302, status)
	return location
}

func (fx *fixture) backAs(token, code, state string) (int, string) {
	fx.t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", itBase+"/api/im-dingtalk/bindings/callback?"+
		url.Values{"authCode": {code}, "state": {state}}.Encode(), nil)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := c.Do(req)
	require.NoError(fx.t, err)
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}

// bind starts a binding as the user and answers the state DingTalk's consent page carries.
func (fx *fixture) bind(c client) string {
	fx.t.Helper()
	code, out := c.do("POST", "/api/im-dingtalk/bindings/start", nil)
	require.Equal(fx.t, 200, code, "%v", out)
	u, err := url.Parse(out["url"].(string))
	require.NoError(fx.t, err)
	return u.Query().Get("state")
}

// bindings: a principal binds their own DingTalk user through OAuth, once per state, in their own signed-in browser
// (a consent another principal started binds nobody; not signed in is refused); a DingTalk user bound to someone else
// is taken; an unknown state fails; the principal reads and ends their binding; binding another DingTalk user ends
// the old binding, both audited; an administrator lists and ends bindings but has no route to set one (ID10; P20.9).
func (fx *fixture) bindings() {
	t := fx.t
	state := fx.bind(fx.bob)
	status, _ := fx.backAs("", "code-bob", state)
	assert.Equal(t, 401, status, "the return is signed in")
	assert.Equal(t, "https://erp.example/me?dingtalk=failed", fx.back(fx.carol, "code-carol", state),
		"bob's consent link finished by carol binds nobody")
	state = fx.bind(fx.bob)
	assert.Equal(t, "https://erp.example/me?dingtalk=bound", fx.back(fx.bob, "code-bob", state))
	assert.Equal(t, "https://erp.example/me?dingtalk=failed", fx.back(fx.bob, "code-bob", state), "a state is used once")
	assert.Equal(t, "https://erp.example/me?dingtalk=failed", fx.back(fx.bob, "code-bob", "forged"))
	code, me := fx.bob.do("GET", "/api/im-dingtalk/bindings/me", nil)
	require.Equal(t, 200, code)
	assert.Equal(t, true, me["bound"])
	assert.Equal(t, "u-bob", me["user_id"])

	assert.Equal(t, "https://erp.example/me?dingtalk=taken", fx.back(fx.carol, "code-carol-as-bob", fx.bind(fx.carol)),
		"carol cannot take bob's DingTalk user")
	assert.Equal(t, "https://erp.example/me?dingtalk=bound", fx.back(fx.carol, "code-carol-2", fx.bind(fx.carol)))
	assert.Equal(t, "https://erp.example/me?dingtalk=bound", fx.back(fx.carol, "code-carol", fx.bind(fx.carol)),
		"another DingTalk user replaces the old binding")
	var ended int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".binding WHERE principal = $1 AND state = 'ended'",
		carol).Scan(&ended))
	assert.Equal(t, 1, ended)
	var audits []string // bound, then the replaced binding's end and the new one, each audited (ID10)
	rows, err := fx.admin.Query(fx.ctx, "SELECT changes->>'binding' FROM "+itSchema+".be_audit_log WHERE target->>'id' = $1 "+
		"ORDER BY occurred_at", carol)
	require.NoError(t, err)
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		audits = append(audits, c)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"bound", "ended", "bound"}, audits)

	code, list := fx.alice.do("GET", "/api/im-dingtalk/bindings", nil)
	require.Equal(t, 200, code)
	assert.Len(t, list["bindings"], 2)
	code, _ = fx.bob.do("GET", "/api/im-dingtalk/bindings", nil)
	assert.Equal(t, 404, code, "not an administrator")
	code, _ = fx.dave.do("POST", "/api/im-dingtalk/bindings/start", nil)
	assert.Equal(t, 404, code, "no grant")
	code, _ = fx.alice.do("PUT", "/api/im-dingtalk/bindings/"+sectest.Bob, map[string]any{"user_id": "u-alice"})
	assert.Equal(t, 501, code, "an administrator cannot set a binding: there is no such route (be-protocol: 501)")

	code, _ = fx.carol.do("DELETE", "/api/im-dingtalk/bindings/me", nil)
	assert.Equal(t, 204, code)
	code, _ = fx.carol.do("DELETE", "/api/im-dingtalk/bindings/me", nil)
	assert.Equal(t, 404, code)
	assert.Equal(t, "https://erp.example/me?dingtalk=bound", fx.back(fx.carol, "code-carol", fx.bind(fx.carol)), "bound again")
	code, _ = fx.alice.do("DELETE", "/api/im-dingtalk/bindings/"+carol, nil)
	assert.Equal(t, 204, code, "an administrator ends one")
	var n int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".bind_session WHERE digest = $1", state).Scan(&n))
	assert.Zero(t, n, "the state is never kept as it is")
}

func (fx *fixture) request(principal string, action bool) *channelpb.DeliverRequest {
	r := &channelpb.DeliverRequest{DeliveryId: ids.New().String(), Sender: "erp/purchase", IntentId: ids.New().String(),
		CollapseKey: "task:x", CollapseSeq: 1, Principal: principal, Category: "approval_task", Title: "采购订单 PO-1 待审批",
		Link: "/workflow/tasks/x", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	if action {
		r.Action = &channelpb.Action{GrantId: ids.New().String(), Choices: []*channelpb.ActionChoice{{Action: "approve", Label: "同意"}}}
	}
	return r
}

// deliveries: the notification component hands a delivery over: accepted for a bound principal, unmapped for one
// without a binding (nothing written); the same delivery again answers the same, with other content
// IDEMPOTENCY_KEY_REUSED; another component may not call; a malformed request is refused (ID01; P20.1).
func (fx *fixture) deliveries() {
	t := fx.t
	r := fx.request(sectest.Bob, true)
	res, err := fx.chann.Deliver(fx.ctx, r)
	require.NoError(t, err)
	assert.Equal(t, channelpb.Outcome_OUTCOME_ACCEPTED, res.GetOutcome())
	res, err = fx.chann.Deliver(fx.ctx, r)
	require.NoError(t, err)
	assert.Equal(t, channelpb.Outcome_OUTCOME_ACCEPTED, res.GetOutcome(), "the same delivery again")
	changed := fx.request(sectest.Bob, true)
	changed.DeliveryId = r.DeliveryId
	_, err = fx.chann.Deliver(fx.ctx, changed)
	e, _, ok := errs.FromGRPC(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, errs.ReasonIdempotencyKeyReused, e.Reason)
	res, err = fx.chann.Deliver(fx.ctx, fx.request(dave, false))
	require.NoError(t, err)
	assert.Equal(t, channelpb.Outcome_OUTCOME_UNMAPPED, res.GetOutcome())
	_, err = fx.other.Deliver(fx.ctx, fx.request(sectest.Bob, false))
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "only the notification component")
	bad := fx.request(sectest.Bob, false)
	bad.Link = "//evil.example"
	_, err = fx.chann.Deliver(fx.ctx, bad)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	dotted := fx.request(sectest.Bob, true)
	dotted.Action.Choices[0].Action = "app.rove" // a dot separates the button's value; the action event's schema refuses it
	_, err = fx.chann.Deliver(fx.ctx, dotted)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	soon := fx.request(sectest.Bob, false) // accepted, then retried after its expiry: the first result (P20.1)
	soon.ExpiresAt = time.Now().Add(1500 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	res, err = fx.chann.Deliver(fx.ctx, soon)
	require.NoError(t, err)
	assert.Equal(t, channelpb.Outcome_OUTCOME_ACCEPTED, res.GetOutcome())
	time.Sleep(2 * time.Second)
	res, err = fx.chann.Deliver(fx.ctx, soon)
	require.NoError(t, err, "a retry past the expiry answers the first result")
	assert.Equal(t, channelpb.Outcome_OUTCOME_ACCEPTED, res.GetOutcome())
}

// sending: an accepted delivery's card goes to the bound DingTalk user with a button per action carrying a one-time
// value (kept as a digest only) and the link button; the delivery's result fact says sent; a refused send says
// rejected (ID02-ID04; P20.2, P20.5).
func (fx *fixture) sending() {
	t := fx.t
	r := fx.request(sectest.Bob, true)
	_, err := fx.chann.Deliver(fx.ctx, r)
	require.NoError(t, err)
	var card core.Card
	require.Eventually(t, func() bool {
		cs := fx.ding.cardsTo(r.DeliveryId)
		if len(cs) > 0 {
			card = cs[0]
		}
		return len(cs) == 1
	}, 20*time.Second, 100*time.Millisecond)
	assert.Equal(t, "u-bob", card.UserID)
	assert.Equal(t, r.Title, card.Title)
	require.Len(t, card.Buttons, 2)
	assert.Equal(t, "approve", card.Buttons[0].Action)
	assert.Equal(t, "https://erp.example/workflow/tasks/x", card.Buttons[1].URL)
	nonce := card.Buttons[0].Value[strings.LastIndex(card.Buttons[0].Value, ".")+1:]
	var stored string
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT actions::text FROM "+itSchema+".delivery WHERE id = $1", r.DeliveryId).Scan(&stored))
	assert.NotContains(t, stored, nonce, "the one-time value is kept as a digest only")
	assert.Contains(t, stored, core.Digest(nonce))
	require.Eventually(t, func() bool { return fx.result(r.DeliveryId) == "sent" }, 20*time.Second, 100*time.Millisecond)

	fx.ding.mu.Lock()
	fx.ding.answers = []string{core.RejectedClass}
	fx.ding.mu.Unlock()
	refused := fx.request(sectest.Bob, false)
	_, err = fx.chann.Deliver(fx.ctx, refused)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return fx.result(refused.DeliveryId) == "rejected" }, 20*time.Second, 100*time.Millisecond)
}

// result is a delivery's result fact as emitted ("" before it).
func (fx *fixture) result(delivery string) string {
	for _, e := range fx.outbox("integration.im_dingtalk_delivery.recorded.v1") {
		if e["delivery_id"] == delivery {
			return e["result"].(string)
		}
	}
	return ""
}

// actionsFor are the action events emitted for a grant.
func (fx *fixture) actionsFor(grant string) []map[string]any {
	var out []map[string]any
	for _, e := range fx.outbox("integration.im_dingtalk_action.recorded.v1") {
		if e["grant_id"] == grant {
			out = append(out, e)
		}
	}
	return out
}

func (fx *fixture) callbackState(event string) (state, reason string) {
	var r *string
	require.Eventually(fx.t, func() bool {
		err := fx.admin.QueryRow(fx.ctx, "SELECT state, reason FROM "+itSchema+".callback WHERE event_id = $1", event).Scan(&state, &r)
		return err == nil && state != "received"
	}, 20*time.Second, 100*time.Millisecond)
	if r != nil {
		reason = *r
	}
	return
}

// callbacks: a button pressed in DingTalk comes back as a signed, encrypted callback: kept and answered success,
// processed into one action event for the delivery's sender (the audience), with the grant; a forged signature or an
// old timestamp is dropped without a row; the same event again is answered and processed once; a card forwarded to
// someone else, an unbound user, a tampered value are quarantined and listed (ID06-ID09; P20.6).
func (fx *fixture) callbacks() {
	t := fx.t
	r := fx.request(sectest.Bob, true)
	_, err := fx.chann.Deliver(fx.ctx, r)
	require.NoError(t, err)
	var value string
	require.Eventually(t, func() bool {
		cs := fx.ding.cardsTo(r.DeliveryId)
		if len(cs) > 0 {
			value = cs[0].Buttons[0].Value
		}
		return value != ""
	}, 20*time.Second, 100*time.Millisecond)
	ev := func(user, value string) map[string]any {
		return map[string]any{"eventId": ids.New().String(), "eventType": "card_callback", "outTrackId": r.DeliveryId,
			"userId": user, "value": value, "clickedAt": time.Now().UnixMilli()}
	}
	e := ev("u-bob", value)
	code, msg := fx.callback(e, 0, cbToken)
	require.Equal(t, 200, code)
	assert.Equal(t, "success", msg)
	st, _ := fx.callbackState(e["eventId"].(string))
	assert.Equal(t, "published", st)
	acts := fx.actionsFor(r.Action.GrantId)
	require.Len(t, acts, 1)
	assert.Equal(t, "erp/purchase", acts[0]["audience"])
	assert.Equal(t, sectest.Bob, acts[0]["principal"])
	assert.Equal(t, "approve", acts[0]["action"])
	assert.Equal(t, core.ID, acts[0]["channel"])
	code, _ = fx.callback(e, 0, cbToken)
	assert.Equal(t, 200, code, "the same event again is answered")
	time.Sleep(500 * time.Millisecond)
	assert.Len(t, fx.actionsFor(r.Action.GrantId), 1, "and processed once")
	var body string
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT body::text FROM "+itSchema+".callback WHERE event_id = $1",
		e["eventId"]).Scan(&body))
	assert.NotContains(t, body, value[strings.LastIndex(value, ".")+1:], "the one-time value is never kept")

	forged := ev("u-bob", value)
	code, _ = fx.callback(forged, 0, "not-the-token")
	assert.Equal(t, 403, code)
	old := ev("u-bob", value)
	code, _ = fx.callback(old, -10*time.Minute, cbToken)
	assert.Equal(t, 403, code)
	var n int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".callback WHERE event_id IN ($1, $2)",
		forged["eventId"], old["eventId"]).Scan(&n))
	assert.Zero(t, n, "dropped without a row")

	assert.Equal(t, "https://erp.example/me?dingtalk=bound", fx.back(fx.carol, "code-carol", fx.bind(fx.carol)))
	for _, c := range []struct {
		event  map[string]any
		reason string
	}{
		{ev("u-carol", value), "not_the_recipient"},
		{ev("u-nobody", value), "not_bound"},
		{ev("u-bob", value[:strings.LastIndex(value, ".")+1]+"tampered"), "unknown_button"},
	} {
		code, _ = fx.callback(c.event, 0, cbToken)
		require.Equal(t, 200, code)
		st, reason := fx.callbackState(c.event["eventId"].(string))
		assert.Equal(t, "quarantined", st)
		assert.Equal(t, c.reason, reason)
	}
	assert.Len(t, fx.actionsFor(r.Action.GrantId), 1, "no action event for them")
	code, list := fx.alice.do("GET", "/api/im-dingtalk/callbacks", nil)
	require.Equal(t, 200, code)
	assert.Len(t, list["callbacks"], 3)
}

// expiry: a send whose result is unknown (DingTalk cannot be queried) is not sent again; past the delivery's expiry it
// fails as expired, never left for a person (ID03; P13.14).
func (fx *fixture) expiry() {
	t := fx.t
	fx.ding.mu.Lock()
	fx.ding.answers = []string{core.MaybeReached}
	fx.ding.mu.Unlock()
	r := fx.request(sectest.Bob, false)
	r.ExpiresAt = time.Now().Add(4 * time.Second).UTC().Format(time.RFC3339Nano)
	_, err := fx.chann.Deliver(fx.ctx, r)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return fx.result(r.DeliveryId) == "expired" }, 30*time.Second, 200*time.Millisecond)
	assert.Len(t, fx.ding.cardsTo(r.DeliveryId), 1, "sent once, not again")
}

// purge: the daily purge deletes each kind of row past its period and keeps the rest (the data): binding sessions a
// day past their expiry, ended bindings after 400 days, deliveries 7 days past their expiry and 90 days after they
// were made, processed callbacks after 180 days; a callback still received is kept.
func (fx *fixture) purge() {
	t := fx.t
	s := itSchema
	exec := func(sql string, args ...any) {
		_, err := fx.admin.Exec(fx.ctx, sql, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO ` + s + `.bind_session (digest, principal, expires_at) VALUES ('old', 'x', now() - interval '2 days'),
		('young', 'x', now() - interval '1 hour')`)
	exec(`INSERT INTO `+s+`.binding (id, principal, user_id, state, ended_at) VALUES ($1, 'p-old', 'u-old', 'ended',
		now() - interval '401 days'), ($2, 'p-young', 'u-young', 'ended', now() - interval '10 days')`, ids.New().String(),
		ids.New().String())
	for _, d := range []struct{ id, expires, created string }{
		{"10000000-0000-7000-8000-000000000001", "8 days", "91 days"},  // purged
		{"10000000-0000-7000-8000-000000000002", "8 days", "30 days"},  // kept: at least 90 days
		{"10000000-0000-7000-8000-000000000003", "6 days", "120 days"}, // kept: not 7 days past its expiry
	} {
		exec(`INSERT INTO `+s+`.delivery (caller, id, digest, sender, intent_id, principal, user_id, category, title, link,
			expires_at, command_id, created_at) VALUES ('infra/notification', $1, 'd', 'erp/purchase', $2, 'p', 'u',
			'approval_task', 't', '/x', now() - $3::interval, $2, now() - $4::interval)`, d.id, ids.New().String(),
			d.expires, d.created)
	}
	for _, c := range []struct{ id, state, at string }{
		{"old-published", "published", "181 days"}, {"old-received", "received", "181 days"}, {"young", "ignored", "10 days"},
	} {
		exec(`INSERT INTO `+s+`.callback (event_id, received_at, cipher_sha, signature, timestamp, nonce, body, state)
			VALUES ($1, now() - $2::interval, 'c', 's', 't', 'n', '{}', $3)`, "purge-"+c.id, c.at, c.state)
	}
	require.NoError(t, dingtalk.Purge(fx.mod.Base(), fx.ctx))
	count := func(sql string) int {
		var n int
		require.NoError(t, fx.admin.QueryRow(fx.ctx, sql).Scan(&n))
		return n
	}
	assert.Zero(t, count(`SELECT count(*) FROM `+s+`.bind_session WHERE digest = 'old'`))
	assert.Equal(t, 1, count(`SELECT count(*) FROM `+s+`.bind_session WHERE digest = 'young'`))
	assert.Zero(t, count(`SELECT count(*) FROM `+s+`.binding WHERE principal = 'p-old'`))
	assert.Equal(t, 1, count(`SELECT count(*) FROM `+s+`.binding WHERE principal = 'p-young'`))
	assert.Equal(t, 2, count(`SELECT count(*) FROM `+s+`.delivery WHERE id::text LIKE '10000000-%'`))
	assert.Zero(t, count(`SELECT count(*) FROM `+s+`.delivery WHERE id = '10000000-0000-7000-8000-000000000001'`))
	assert.Equal(t, 2, count(`SELECT count(*) FROM `+s+`.callback WHERE event_id LIKE 'purge-%'`))
	assert.Zero(t, count(`SELECT count(*) FROM `+s+`.callback WHERE event_id = 'purge-old-published'`))
}
