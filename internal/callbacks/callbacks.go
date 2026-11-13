// Package callbacks is DingTalk's callbacks (docs/design.md ID06-ID09; be-protocol P20.6): a public route that
// verifies the signature (and the time window), decrypts, deduplicates, keeps the callback as received with a work item
// in one transaction and only then answers success; the work item processes it once — the delivery, the binding, the
// card's recipient, the button's one-time value — and publishes the action event or quarantines it.
package callbacks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	bindings "github.com/brickKit/integration-im-dingtalk/v3/internal/bindings"
	core "github.com/brickKit/integration-im-dingtalk/v3/internal/core"
	"github.com/brickKit/integration-im-dingtalk/v3/internal/deliveries"
	"github.com/brickKit/integration-im-dingtalk/v3/internal/dingtalk"

	"github.com/brickKit/be-sdk-go/v3/channel"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/work"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// JobProcess processes one received callback (ID07); queued in the transaction that kept it.
const JobProcess = "process-callback"

// The callbacks' parameters (ID06, ID07).
const (
	Window   = 5 * time.Minute // a timestamp further than this from now is refused
	MaxBody  = 64 << 10
	StuckFor = 5 * time.Minute // received and not processed for this long: alerted
)

// Counters count what the route drops (ID06): no row is written for them.
type Counters struct {
	Dropped *prometheus.CounterVec
}

// NewCounters registers the counters on the member's registry (nil: none).
func NewCounters(r prometheus.Registerer) *Counters {
	c := &Counters{Dropped: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "dingtalk_callbacks_dropped_total",
		Help: "DingTalk callbacks dropped before anything was written (ID06), by why."}, []string{"why"})}
	if r != nil {
		_ = r.Register(c.Dropped)
	}
	return c
}

// event is the decrypted callback as this component reads it: a card's button (the modelled shape; ID06).
type event struct {
	EventID    string `json:"eventId"`
	EventType  string `json:"eventType"`
	OutTrackID string `json:"outTrackId"`
	UserID     string `json:"userId"`
	Value      string `json:"value"` // <delivery>.<action>.<one-time value>
	ClickedAt  int64  `json:"clickedAt"`
}

// Receive is the public callback route (ID06): dropped and counted when the signature or the window is wrong; a
// callback seen before answers success again; otherwise kept as received with its work item, then answered.
func Receive(m *core.Base, c *Counters, h *httpx.Context) error {
	q := func(k string) string { return h.Query(k) }
	sig, ts, nonce := q("signature"), q("timestamp"), q("nonce")
	drop := func(why string) error {
		if c != nil {
			c.Dropped.WithLabelValues(why).Inc()
		}
		return errs.New(errs.ReasonPermissionDenied, "the callback is not DingTalk's")
	}
	raw, err := io.ReadAll(io.LimitReader(h.Request().Body, MaxBody+1))
	if err != nil || len(raw) > MaxBody {
		return drop("size")
	}
	var envelope struct {
		Encrypt string `json:"encrypt"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Encrypt == "" {
		return drop("shape")
	}
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || absDuration(time.Since(time.UnixMilli(ms))) > Window {
		return drop("window")
	}
	if !dingtalk.Verify(m.Cfg.CallbackToken(), ts, nonce, envelope.Encrypt, sig) {
		return drop("signature")
	}
	plain, err := dingtalk.Decrypt(m.Cfg.AESKey(), m.Cfg.AppKey, envelope.Encrypt)
	if err != nil {
		return drop("decrypt")
	}
	var e event
	if json.Unmarshal(plain, &e) != nil || e.EventID == "" {
		return drop("event")
	}
	kept := redact(e)
	body, _ := json.Marshal(kept)
	sum := sha256.Sum256([]byte(envelope.Encrypt))
	err = m.RT.DB.Tx(h.Ctx(), func(ctx context.Context, tx db.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO callback (event_id, cipher_sha, signature, timestamp, nonce, body, state)
			VALUES ($1, $2, $3, $4, $5, $6, 'received') ON CONFLICT (event_id) DO NOTHING`, e.EventID,
			hex.EncodeToString(sum[:]), sig, ts, nonce, body)
		if err != nil || tag.RowsAffected() == 0 { // seen before: answered again, processed once
			return err
		}
		_, err = m.RT.Work.Enqueue(ctx, tx, JobProcess, map[string]string{"event_id": e.EventID})
		return err
	})
	if err != nil { // not kept: DingTalk pushes it again
		return err
	}
	return answer(m, h)
}

// answer is the encrypted "success" DingTalk expects.
func answer(m *core.Base, h *httpx.Context) error {
	enc, err := dingtalk.Encrypt(m.Cfg.AESKey(), m.Cfg.AppKey, []byte("success"))
	if err != nil {
		return err
	}
	ts, nonce := strconv.FormatInt(time.Now().UnixMilli(), 10), ids.New().String()[:8]
	return h.JSON(200, map[string]string{"msg_signature": dingtalk.Sign(m.Cfg.CallbackToken(), ts, nonce, enc),
		"timeStamp": ts, "nonce": nonce, "encrypt": enc})
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// redact replaces the button's one-time value with its digest: never kept as it came (ID06).
func redact(e event) event {
	if parts := strings.SplitN(e.Value, ".", 3); len(parts) == 3 {
		e.Value = parts[0] + "." + parts[1] + "." + core.Digest(parts[2])
	} else if e.Value != "" {
		e.Value = core.Digest(e.Value)
	}
	return e
}

// Process is the work item (ID07): once per callback (the row's state says), in one transaction, the action event
// published or the callback quarantined with its reason.
func Process(m *core.Base, ctx context.Context, r *work.Run) error {
	var args struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(r.Args, &args); err != nil {
		return err
	}
	return m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var raw []byte
		var state string
		err := tx.QueryRow(ctx, `SELECT body, state FROM callback WHERE event_id = $1 FOR UPDATE`, args.EventID).
			Scan(&raw, &state)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && state != "received") {
			return nil // processed already
		}
		if err != nil {
			return err
		}
		var e event
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		next, reason, err := judge(m, ctx, tx, e)
		if err != nil {
			return err
		}
		if next == "quarantined" {
			m.RT.Logger.Warn("a DingTalk callback was quarantined", "event", "dingtalk_callback_quarantined",
				"callback", e.EventID, "reason", reason)
		}
		_, err = tx.Exec(ctx, `UPDATE callback SET state = $2, reason = $3, processed_at = now() WHERE event_id = $1`,
			e.EventID, next, nullable(reason))
		return err
	})
}

// judge decides one received callback (ID07) and, when it holds, emits the action event (ID08).
func judge(m *core.Base, ctx context.Context, tx db.Tx, e event) (string, string, error) {
	if e.EventType != "card_callback" {
		return "ignored", "", nil
	}
	var d struct {
		caller, sender, principal, user string
		grant                           *string
		actions                         []deliveries.Action
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT caller, sender, principal, user_id, grant_id::text, actions FROM delivery WHERE id::text = $1`,
		e.OutTrackID).Scan(&d.caller, &d.sender, &d.principal, &d.user, &d.grant, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "quarantined", "unknown_delivery", nil
	}
	if err != nil {
		return "", "", err
	}
	_ = json.Unmarshal(raw, &d.actions)
	principal, err := bindings.PrincipalOf(ctx, tx, e.UserID)
	switch {
	case err != nil:
		return "", "", err
	case principal == "":
		return "quarantined", "not_bound", nil
	case principal != d.principal:
		return "quarantined", "not_the_recipient", nil // the card was forwarded to someone else
	}
	parts := strings.SplitN(e.Value, ".", 3) // <delivery>.<action>.<one-time value's digest>
	if len(parts) != 3 || parts[0] != e.OutTrackID || d.grant == nil {
		return "quarantined", "not_a_button", nil
	}
	var known bool
	for _, a := range d.actions {
		if a.Action == parts[1] && a.Nonce != "" && a.Nonce == parts[2] {
			known = true
		}
	}
	if !known {
		return "quarantined", "unknown_button", nil
	}
	clicked := time.UnixMilli(e.ClickedAt)
	if e.ClickedAt == 0 {
		clicked = time.Now()
	}
	a := channel.Action{ID: ids.New().String(), Channel: core.ID, Audience: d.sender, Principal: principal, GrantID: *d.grant,
		Action: parts[1], ClickedAt: clicked.UTC().Format(time.RFC3339Nano)}
	if err := channel.EmitAction(ctx, tx, m.RT.Events, a); err != nil {
		return "", "", err
	}
	return "published", "", nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Stuck alerts the callbacks received and not processed for StuckFor (ID07): every minute.
func Stuck(m *core.Base, ctx context.Context, _ *work.Run) error {
	return m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM callback WHERE state = 'received' AND received_at < now() - $1::interval`,
			StuckFor.String()).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			m.RT.Logger.Error("DingTalk callbacks are not processed", "event", "dingtalk_callbacks_stuck", "count", n)
		}
		return nil
	})
}

// Quarantined lists the quarantined callbacks for an administrator (ID09), the latest first.
func Quarantined(m *core.Base, h *httpx.Context) error {
	ctx, _, err := core.Check(h, core.OpCallbackRead)
	if err != nil {
		return err
	}
	type row struct {
		EventID    string `json:"event_id"`
		Reason     string `json:"reason"`
		ReceivedAt string `json:"received_at"`
	}
	out := []row{}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		rows, err := tx.Query(ctx, `SELECT event_id, coalesce(reason, ''), received_at FROM callback WHERE state = 'quarantined'
			ORDER BY received_at DESC, event_id DESC LIMIT 200`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r row
			var at time.Time
			if err := rows.Scan(&r.EventID, &r.Reason, &at); err != nil {
				return err
			}
			r.ReceivedAt = at.UTC().Format(time.RFC3339Nano)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	return h.JSON(200, map[string]any{"callbacks": out})
}
