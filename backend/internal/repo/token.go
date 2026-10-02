package repo

import (
	"context"
	"database/sql"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

// Token 是 dingtalk_token 单行表的内容。落库而不是只放内存：重启后重新
// 申请会撞钉钉 gettoken 的频控。
type Token struct {
	AccessToken string
	ExpiresAt   time.Time // 零值表示从未成功获取过
}

// GetToken 读当前缓存的 token（可能已过期；该不该刷新由 tokenmgr 判断）。
func (r *Repo) GetToken(ctx context.Context) (Token, error) {
	var t Token
	var expiresAt sql.NullTime
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT access_token, expires_at FROM dingtalk_token WHERE id = 1`).
			Scan(&t.AccessToken, &expiresAt)
	})
	if expiresAt.Valid {
		t.ExpiresAt = expiresAt.Time
	}
	return t, wrap("读 access_token 缓存", err)
}

// SetToken 覆盖单行缓存（同一个 id=1，见迁移的 CHECK 约束）。
func (r *Repo) SetToken(ctx context.Context, accessToken string, expiresAt time.Time) error {
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE dingtalk_token SET access_token = $1, expires_at = $2, updated_at = now() WHERE id = 1`,
			accessToken, expiresAt)
		return err
	})
	return wrap("写 access_token 缓存", err)
}
