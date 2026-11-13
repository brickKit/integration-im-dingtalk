// Package deliveries is the deliveries the notification component hands over (docs/design.md ID01-ID04; be-protocol
// P20.1, P20.2, P20.5): be.channel.v1.ChannelService.Deliver, idempotent by the caller and the delivery id; each one an
// external command sending an interactive card, its buttons' one-time values made at each send and kept as digests
// only; the result fact when the command ends.
package deliveries

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	bindings "github.com/brickKit/integration-im-dingtalk/v3/internal/bindings"
	core "github.com/brickKit/integration-im-dingtalk/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/channel"
	"github.com/brickKit/be-sdk-go/v3/channelpb"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/extcmd"
	"github.com/brickKit/be-sdk-go/v3/grpcx"
	"github.com/jackc/pgx/v5"
)

// categories are the shared catalog (be-protocol P18.3) with each one's line on the card.
var categories = map[string]string{"approval_task": "待审批", "due_reminder": "到期提醒", "security_alert": "安全提醒",
	"legal_notice": "法律通知", "external_comm": "对外通信", "marketing": "营销"}

// Action is one button of a delivery as kept: the action, its label and its one-time value's digest of the last send.
type Action struct {
	Action string `json:"action"`
	Label  string `json:"label"`
	Nonce  string `json:"nonce_digest,omitempty"`
}

// Deliver takes a delivery in (ID01): the same caller and delivery id answer the first result, other content
// IDEMPOTENCY_KEY_REUSED; a principal without an active binding is unmapped and nothing is written; otherwise the
// delivery and its external command (abandoned at the delivery's expiry, P13.14) are written, accepted.
func Deliver(m *core.Base, ctx context.Context, in *channelpb.DeliverRequest) (*channelpb.DeliverResponse, error) {
	caller, ok := grpcx.Caller(ctx)
	if !ok {
		return nil, errs.New(errs.ReasonPermissionDenied, "no calling service")
	}
	if err := valid(in); err != nil {
		return nil, err
	}
	digest, err := canon.Digest("integration.im_dingtalk.delivery", 1, request(in))
	if err != nil {
		return nil, err
	}
	expires, _ := time.Parse(time.RFC3339Nano, in.GetExpiresAt())
	out := &channelpb.DeliverResponse{}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var stored string
		err := tx.QueryRow(ctx, `SELECT digest FROM delivery WHERE caller = $1 AND id = $2::uuid`, caller, in.GetDeliveryId()).
			Scan(&stored)
		switch {
		case err == nil && stored != digest:
			return errs.New(errs.ReasonIdempotencyKeyReused, "this delivery id was handed over with other content")
		case err == nil:
			out.Outcome = channelpb.Outcome_OUTCOME_ACCEPTED
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		user, err := bindings.UserOf(ctx, tx, in.GetPrincipal())
		if err != nil {
			return err
		}
		if user == "" {
			out.Outcome = channelpb.Outcome_OUTCOME_UNMAPPED
			return nil
		}
		var actions []Action
		var grant *string
		if a := in.GetAction(); a != nil {
			g := a.GetGrantId()
			grant = &g
			for _, ch := range a.GetChoices() {
				actions = append(actions, Action{Action: ch.GetAction(), Label: ch.GetLabel()})
			}
		}
		acts, _ := json.Marshal(actions)
		if actions == nil {
			acts = []byte("[]")
		}
		cmd, _, err := m.RT.External.RequestUntil(ctx, tx, caller, in.GetDeliveryId(), core.SendCard,
			map[string]any{"delivery_id": in.GetDeliveryId(), "caller": caller}, nil, &expires)
		if err != nil {
			return err
		}
		var key *string
		var seq *int64
		if in.GetCollapseKey() != "" {
			k, s := in.GetCollapseKey(), in.GetCollapseSeq()
			key, seq = &k, &s
		}
		if _, err := tx.Exec(ctx, `INSERT INTO delivery (caller, id, digest, sender, intent_id, collapse_key, collapse_seq,
				principal, user_id, category, title, link, expires_at, grant_id, actions, command_id)
			VALUES ($1, $2::uuid, $3, $4, $5::uuid, $6, $7, $8, $9, $10, $11, $12, $13, $14::uuid, $15, $16::uuid)`,
			caller, in.GetDeliveryId(), digest, in.GetSender(), in.GetIntentId(), key, seq, in.GetPrincipal(), user,
			in.GetCategory(), in.GetTitle(), in.GetLink(), expires, grant, acts, cmd.ID); err != nil {
			return err
		}
		out.Outcome = channelpb.Outcome_OUTCOME_ACCEPTED
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// request is what the idempotency compares.
func request(in *channelpb.DeliverRequest) map[string]any {
	r := map[string]any{"sender": in.GetSender(), "intent_id": in.GetIntentId(), "principal": in.GetPrincipal(),
		"category": in.GetCategory(), "title": in.GetTitle(), "link": in.GetLink(), "expires_at": in.GetExpiresAt(),
		"collapse_key": in.GetCollapseKey(), "collapse_seq": in.GetCollapseSeq()}
	if a := in.GetAction(); a != nil {
		var cs []any
		for _, c := range a.GetChoices() {
			cs = append(cs, map[string]any{"action": c.GetAction(), "label": c.GetLabel()})
		}
		r["grant_id"], r["choices"] = a.GetGrantId(), cs
	}
	return r
}

// valid checks the request as be-protocol P18.3, P18.5 and P20.1 say.
func valid(in *channelpb.DeliverRequest) error {
	exp, err := time.Parse(time.RFC3339Nano, in.GetExpiresAt())
	switch {
	case !uuidForm(in.GetDeliveryId()):
		return core.Invalid("delivery_id", "a UUID")
	case in.GetPrincipal() == "" || in.GetSender() == "" || !uuidForm(in.GetIntentId()):
		return core.Invalid("principal", "the principal, the sender and the intent are required")
	case categories[in.GetCategory()] == "":
		return core.Invalid("category", "not a category of the catalog")
	case len([]rune(in.GetTitle())) < 1 || len([]rune(in.GetTitle())) > 200:
		return core.Invalid("title", "1 to 200 characters")
	case !linkForm(in.GetLink()):
		return core.Invalid("link", "a path in the system")
	case err != nil:
		return core.Invalid("expires_at", "RFC 3339")
	case !exp.After(time.Now()):
		return core.Invalid("expires_at", "already expired")
	}
	if a := in.GetAction(); a != nil {
		if !uuidForm(a.GetGrantId()) || len(a.GetChoices()) == 0 || len(a.GetChoices()) > 5 {
			return core.Invalid("action", "a grant and one to five actions")
		}
		for _, c := range a.GetChoices() {
			if c.GetAction() == "" || c.GetLabel() == "" || len([]rune(c.GetLabel())) > 40 {
				return core.Invalid("action", "each action has a name and a label of at most 40 characters")
			}
		}
	}
	return nil
}

func linkForm(l string) bool {
	return len(l) >= 1 && len(l) <= 2000 && l[0] == '/' && (len(l) == 1 || (l[1] != '/' && l[1] != '\\'))
}

func uuidForm(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// Adapter is the external command's side (ID03): each send makes the buttons' one-time values anew and keeps their
// digests, then sends the card; nothing can be queried (ID11).
type Adapter struct{ M *core.Base }

// Send sends one delivery's card.
func (a Adapter) Send(ctx context.Context, c extcmd.Command) (extcmd.Result, error) {
	m := a.M
	caller, _ := c.Params["caller"].(string)
	id, _ := c.Params["delivery_id"].(string)
	var card core.Card
	var link, category string
	var actions []Action
	if err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT user_id, title, link, category, actions FROM delivery WHERE caller = $1
			AND id = $2::uuid FOR UPDATE`, caller, id).Scan(&card.UserID, &card.Title, &link, &category, &raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &actions); err != nil {
			return err
		}
		card.OutTrackID, card.Body = id, categories[category]
		for i := range actions { // a new one-time value per send: the digest replaces the last one (ID03)
			value, digest := core.NewSecret()
			actions[i].Nonce = digest
			card.Buttons = append(card.Buttons, core.Button{Label: actions[i].Label, Action: actions[i].Action,
				Value: id + "." + actions[i].Action + "." + value})
		}
		b, _ := json.Marshal(actions)
		_, err := tx.Exec(ctx, `UPDATE delivery SET actions = $3 WHERE caller = $1 AND id = $2::uuid`, caller, id, b)
		return err
	}); err != nil {
		return extcmd.Result{Class: extcmd.NotReached, Response: "the delivery could not be read"}, nil
	}
	card.Buttons = append(card.Buttons, core.Button{Label: "在系统中处理", URL: m.Cfg.LinkBase + link})
	class, response := m.Client.SendCard(ctx, card)
	return extcmd.Result{Class: class, Response: response}, nil
}

// Query: DingTalk cannot be asked about a card (ID11): the query is unavailable, the command waits for its abandon
// time.
func (Adapter) Query(context.Context, extcmd.Command) (extcmd.Answer, error) {
	return extcmd.Answer{}, errors.New("dingtalk: a card cannot be queried")
}

// Capabilities are the external system's (ID11): no dedupe, no query, no request expiry; not irrevocable.
func Capabilities() map[string]extcmd.Capabilities {
	return map[string]extcmd.Capabilities{core.SendCard: {}}
}

// Settled records a delivery's result and emits its result fact, in the transaction that ended its command (ID04).
func Settled(m *core.Base) func(ctx context.Context, tx db.Tx, c extcmd.Command, state, why string) error {
	return func(ctx context.Context, tx db.Tx, c extcmd.Command, state, why string) error {
		caller, _ := c.Params["caller"].(string)
		id, _ := c.Params["delivery_id"].(string)
		result := map[string]string{extcmd.Succeeded: channel.Sent, extcmd.Rejected: channel.Rejected}[state]
		if result == "" {
			result = channel.Failed
			if why == extcmd.WhyExpired {
				result = channel.Expired
			}
		}
		var d channel.Delivery
		if err := tx.QueryRow(ctx, `UPDATE delivery SET result = $3 WHERE caller = $1 AND id = $2::uuid
			RETURNING sender, intent_id::text, principal`, caller, id, result).Scan(&d.Sender, &d.IntentID, &d.Principal); err != nil {
			return err
		}
		d.DeliveryID, d.Caller, d.Result = id, caller, result
		return channel.EmitDelivery(ctx, tx, m.RT.Events, core.ID, d)
	}
}
