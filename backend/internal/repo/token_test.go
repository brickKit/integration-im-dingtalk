package repo

import (
	"context"
	"testing"
	"time"
)

func TestToken_读写单行(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()

	// 迁移已经 INSERT 了 id=1 那一行（空 access_token），先确认初始态。
	initial, err := r.GetToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = initial // 不断言具体值——可能被其它测试并发改写，只验证读不报错

	expiresAt := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	if err := r.SetToken(ctx, "tok-abc", expiresAt); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "tok-abc" {
		t.Fatalf("期望 tok-abc，实际 %q", got.AccessToken)
	}
	if !got.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("期望 expires_at=%v，实际 %v", expiresAt, got.ExpiresAt)
	}
}
