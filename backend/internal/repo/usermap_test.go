package repo

import (
	"context"
	"testing"
)

func TestUserMap_写入命中_删除后未命中(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	phone := uniqueID("phone")

	if _, ok, err := r.GetUserID(ctx, phone); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("从没写过，期望 ok=false")
	}

	if err := r.SetUserID(ctx, phone, "u-1"); err != nil {
		t.Fatal(err)
	}
	userid, ok, err := r.GetUserID(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || userid != "u-1" {
		t.Fatalf("期望 ok=true userid=u-1，实际 ok=%v userid=%q", ok, userid)
	}

	// 设计计划 §2 的过期策略：发送失败且"用户不存在"时删掉这一行。
	if err := r.DeleteUserID(ctx, phone); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := r.GetUserID(ctx, phone); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("删除之后期望 ok=false")
	}
}

func TestUserMap_重复写入覆盖(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	phone := uniqueID("phone")

	if err := r.SetUserID(ctx, phone, "u-old"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetUserID(ctx, phone, "u-new"); err != nil {
		t.Fatal(err)
	}
	userid, ok, err := r.GetUserID(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || userid != "u-new" {
		t.Fatalf("期望覆盖成 u-new，实际 ok=%v userid=%q", ok, userid)
	}
}
