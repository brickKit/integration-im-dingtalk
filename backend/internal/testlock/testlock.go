// Package testlock 只给测试用（只被 _test.go 导入，不进组件二进制）。
//
// dingtalk_token 是单行表（id = 1）：repo、tokenmgr、consumer 三个包的测试
// 都会读写它，而 go test ./... 把不同包的测试二进制并行跑——一个包刚写进去
// 的 token 被另一个包覆盖，断言随机失败（实测：repo 的 TestToken_读写单行
// 读回 ""，因为 tokenmgr 的测试同时把缓存清空了）。TokenRow 在测试期间
// 持有一把 PostgreSQL advisory lock，让碰这一行的测试跨包串行。
package testlock

import (
	"context"
	"database/sql"
	"testing"
)

const tokenRowLock = `hashtext('integration_im_dingtalk.dingtalk_token')`

// TokenRow 拿锁直到测试结束（t.Cleanup 里释放）。锁挂在一条专用连接上：
// advisory lock 属于会话，换一条池里的连接就不是同一把锁。
func TokenRow(t testing.TB, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("取连接给 dingtalk_token 测试锁: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(`+tokenRowLock+`)`); err != nil {
		_ = conn.Close()
		t.Fatalf("拿 dingtalk_token 测试锁: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock(`+tokenRowLock+`)`)
		_ = conn.Close()
	})
}
