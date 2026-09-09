package repo

import (
	"context"
	"database/sql"

	besdk "github.com/brickKit/be-sdk-go"
)

// GetUserID 查手机号→钉钉 userid 的缓存（设计计划 §2：钉钉那个查询接口
// 有频控，不能每发一条查一次）。ok=false 表示没缓存过，调用方该去调
// 钉钉的 getbymobile。
func (r *Repo) GetUserID(ctx context.Context, phone string) (userid string, ok bool, err error) {
	err = besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		e := tx.QueryRowContext(ctx, `SELECT userid FROM dingtalk_user_map WHERE phone = $1`, phone).Scan(&userid)
		if e == sql.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		ok = true
		return nil
	})
	return userid, ok, wrap("查手机号缓存", err)
}

// SetUserID 写入/刷新缓存。
func (r *Repo) SetUserID(ctx context.Context, phone, userid string) error {
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `
			INSERT INTO dingtalk_user_map (phone, userid, resolved_at)
			VALUES ($1, $2, now())
			ON CONFLICT (phone) DO UPDATE SET userid = EXCLUDED.userid, resolved_at = now()`,
			phone, userid)
		return e
	})
	return wrap("写手机号缓存", err)
}

// DeleteUserID 是设计计划 §2 明文要求的过期策略：钉钉返回"用户不存在"
// 时删掉这一行，下次重新查（不用定时全量刷新——那会把频控吃光）。
func (r *Repo) DeleteUserID(ctx context.Context, phone string) error {
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `DELETE FROM dingtalk_user_map WHERE phone = $1`, phone)
		return e
	})
	return wrap("清理失效手机号缓存", err)
}
