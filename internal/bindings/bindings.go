// Package bindings is the principals' DingTalk accounts (docs/design.md ID10; be-protocol P20.9): bound only by the
// principal through DingTalk's OAuth (a binding session's state, one use, ten minutes), looked up by the deliveries
// and the callbacks, ended by the principal or an administrator; never set by an administrator (whoever could would
// approve as someone else).
package bindings

import (
	"context"
	"errors"
	"net/url"
	"time"

	core "github.com/brickKit/integration-im-dingtalk/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/audit"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/jackc/pgx/v5"
)

// errTaken is a DingTalk user bound to another account already.
var errTaken = errors.New("bindings: this DingTalk user is bound to another account")

// SessionLife is how long a binding's state is good for (ID10).
const SessionLife = 10 * time.Minute

// UserOf is a principal's active DingTalk user, "" without one.
func UserOf(ctx context.Context, tx db.Tx, principal string) (string, error) {
	var u string
	err := tx.QueryRow(ctx, `SELECT user_id FROM binding WHERE principal = $1 AND state = 'active'`, principal).Scan(&u)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return u, err
}

// PrincipalOf is a DingTalk user's active principal, "" without one.
func PrincipalOf(ctx context.Context, tx db.Tx, user string) (string, error) {
	var p string
	err := tx.QueryRow(ctx, `SELECT principal FROM binding WHERE user_id = $1 AND state = 'active'`, user).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return p, err
}

// Start opens a binding session for the caller and answers DingTalk's consent page (ID10).
func Start(m *core.Base, c *httpx.Context) error {
	ctx, me, err := core.Check(c, core.OpBindSelf)
	if err != nil {
		return err
	}
	state, digest := core.NewSecret()
	if err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO bind_session (digest, principal, expires_at) VALUES ($1, $2, $3)`, digest, me,
			time.Now().Add(SessionLife))
		return err
	}); err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"url": m.Client.ConsentURL(state)})
}

// Callback is DingTalk's redirect back with the code and the state (ID10): a signed-in route, so only the principal who
// started the binding can finish it (a consent link passed to someone else binds nobody, P20.9); the state is used
// once, the code exchanged for the DingTalk user, the binding made; the browser goes back to the front end with the
// outcome.
func Callback(m *core.Base, c *httpx.Context) error {
	ctx, me, err := core.Check(c, core.OpBindSelf)
	if err != nil {
		return err
	}
	code, state := c.Query("authCode"), c.Query("state")
	if code == "" {
		code = c.Query("code")
	}
	outcome := "bound"
	if err := bind(m, ctx, me, code, state); err != nil {
		if errors.Is(err, errTaken) {
			outcome = "taken"
		} else {
			outcome = "failed"
			m.RT.Logger.Warn("a DingTalk binding failed", "event", "dingtalk_binding_failed", "error", err.Error())
		}
	}
	return c.Redirect(302, m.Cfg.BindReturn+"?"+url.Values{"dingtalk": {outcome}}.Encode())
}

func bind(m *core.Base, ctx context.Context, me, code, state string) error {
	if code == "" || state == "" {
		return core.Invalid("state", "the code and the state are required")
	}
	digest := core.Digest(state)
	var principal string
	if err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error { // the state, used once, atomically
		return tx.QueryRow(ctx, `UPDATE bind_session SET used_at = now() WHERE digest = $1 AND used_at IS NULL
			AND expires_at > now() AND principal = $2 RETURNING principal`, digest, me).Scan(&principal)
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return core.NotFound("binding session")
		}
		return err
	}
	user, err := m.Client.UserOfCode(ctx, code) // outside any transaction
	if err != nil {
		return err
	}
	return m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		if other, err := PrincipalOf(ctx, tx, user); err != nil {
			return err
		} else if other != "" && other != principal {
			return errTaken
		} else if other == principal {
			return nil // bound already
		}
		ended, err := tx.Exec(ctx, `UPDATE binding SET state = 'ended', ended_at = now(), ended_by = $1
			WHERE principal = $1 AND state = 'active'`, principal)
		if err != nil {
			return err
		}
		if ended.RowsAffected() > 0 { // a new DingTalk user replaces the old: the end is audited too (ID10)
			if err := change(m, ctx, tx, principal, core.OpBindSelf, "ended", principal); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO binding (id, principal, user_id, state) VALUES ($1, $2, $3, 'active')`,
			ids.New().String(), principal, user); err != nil {
			return err
		}
		return change(m, ctx, tx, principal, core.OpBindSelf, "bound", principal)
	})
}

func change(m *core.Base, ctx context.Context, tx db.Tx, actor, op, what, principal string) error {
	if m.RT.Audit == nil {
		return nil
	}
	_, err := m.RT.Audit.Write(ctx, tx, audit.Record{Category: audit.BusinessChange, Operation: op, Outcome: audit.Succeeded,
		Actor: actor, Target: audit.TargetOf(core.Bindings, principal, nil), Changes: map[string]any{"binding": what}})
	return err
}

// Mine answers the caller's binding (ID10).
func Mine(m *core.Base, c *httpx.Context) error {
	ctx, me, err := core.Check(c, core.OpBindSelf)
	if err != nil {
		return err
	}
	var user string
	var at *time.Time
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		err := tx.QueryRow(ctx, `SELECT user_id, bound_at FROM binding WHERE principal = $1 AND state = 'active'`, me).
			Scan(&user, &at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	if user == "" {
		return c.JSON(200, map[string]any{"bound": false})
	}
	return c.JSON(200, map[string]any{"bound": true, "user_id": user, "bound_at": at.UTC().Format(time.RFC3339Nano)})
}

// UnbindMine ends the caller's binding (ID10).
func UnbindMine(m *core.Base, c *httpx.Context) error {
	ctx, me, err := core.Check(c, core.OpBindSelf)
	if err != nil {
		return err
	}
	return end(m, c, ctx, me, me, core.OpBindSelf)
}

// End is an administrator ending a principal's binding (ID10): ending only, never setting one.
func End(m *core.Base, c *httpx.Context) error {
	ctx, me, err := core.Check(c, core.OpBindEnd)
	if err != nil {
		return err
	}
	return end(m, c, ctx, me, c.Param("principal"), core.OpBindEnd)
}

func end(m *core.Base, c *httpx.Context, ctx context.Context, actor, principal, op string) error {
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE binding SET state = 'ended', ended_at = now(), ended_by = $2
			WHERE principal = $1 AND state = 'active'`, principal, actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("binding")
		}
		return change(m, ctx, tx, actor, op, "ended", principal)
	})
	if err != nil {
		return err
	}
	return c.NoContent(204)
}

// List lists the active bindings for an administrator (ID10), by principal, a page at a time.
func List(m *core.Base, c *httpx.Context) error {
	ctx, _, err := core.Check(c, core.OpBindRead)
	if err != nil {
		return err
	}
	after := c.Query("cursor")
	type row struct {
		Principal string `json:"principal"`
		UserID    string `json:"user_id"`
		BoundAt   string `json:"bound_at"`
	}
	out := []row{}
	const limit = 200
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		rows, err := tx.Query(ctx, `SELECT principal, user_id, bound_at FROM binding WHERE state = 'active' AND principal > $1
			ORDER BY principal LIMIT $2`, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r row
			var at time.Time
			if err := rows.Scan(&r.Principal, &r.UserID, &at); err != nil {
				return err
			}
			r.BoundAt = at.UTC().Format(time.RFC3339Nano)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].Principal
	}
	return c.JSON(200, map[string]any{"bindings": out, "next_cursor": next})
}

// Purge deletes the binding sessions a day after their expiry and the ended bindings after 400 days (the data), in
// the daily purge's transaction.
func Purge(ctx context.Context, tx db.Tx) error {
	if _, err := tx.Exec(ctx, `DELETE FROM bind_session WHERE expires_at < now() - $1::interval`, core.SessionKept.String()); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM binding WHERE state = 'ended' AND ended_at < now() - $1::interval`,
		core.EndedBindingKept.String())
	return err
}
