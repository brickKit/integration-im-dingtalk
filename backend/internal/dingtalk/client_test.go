package dingtalk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 这些测试用 httptest.Server 模拟钉钉开放平台的接口形状（按公开文档写的
// 请求/响应约定），验证的是本组件自己的请求构造与响应解析逻辑——不是
// "钉钉真的这样回"，那部分按既定决策（03-阶段三 Task 9）本阶段跳过，
// 等有真实测试企业时要再补一次真机验证。

func TestFetchAccessToken_解析成功响应(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gettoken" {
			t.Fatalf("期望路径 /gettoken，实际 %s", r.URL.Path)
		}
		if r.URL.Query().Get("appkey") != "ak1" || r.URL.Query().Get("appsecret") != "as1" {
			t.Fatalf("appkey/appsecret 参数不对：%v", r.URL.Query())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "access_token": "tok-1", "expires_in": 7200})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ak1", "as1")
	token, ttl, err := c.FetchAccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "tok-1" {
		t.Fatalf("期望 token=tok-1，实际 %q", token)
	}
	if ttl != 7200*time.Second {
		t.Fatalf("期望 ttl=7200s，实际 %v", ttl)
	}
}

func TestFetchAccessToken_errcode非零返回APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 40001, "errmsg": "invalid appsecret"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ak1", "bad-secret")
	_, _, err := c.FetchAccessToken(context.Background())
	if err == nil {
		t.Fatal("期望返回错误")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("期望 *APIError，实际 %T: %v", err, err)
	}
	if apiErr.Code != 40001 {
		t.Fatalf("期望 errcode=40001，实际 %d", apiErr.Code)
	}
}

func TestGetUserIDByMobile_请求体与响应解析(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("期望 POST，实际 %s", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["mobile"] != "13800000000" {
			t.Fatalf("期望 mobile=13800000000，实际 %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errcode": 0, "errmsg": "ok", "result": map[string]string{"userid": "u123"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ak", "as")
	userid, err := c.GetUserIDByMobile(context.Background(), "tok", "13800000000")
	if err != nil {
		t.Fatal(err)
	}
	if userid != "u123" {
		t.Fatalf("期望 userid=u123，实际 %q", userid)
	}
}

func TestSendWorkNotification_请求体形状(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["agent_id"].(float64) != 123 {
			t.Fatalf("期望 agent_id=123，实际 %v", body["agent_id"])
		}
		if body["userid_list"] != "u123" {
			t.Fatalf("期望 userid_list=u123，实际 %v", body["userid_list"])
		}
		msg, _ := body["msg"].(map[string]any)
		if msg["msgtype"] != "text" {
			t.Fatalf("期望 msgtype=text，实际 %v", msg)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "task_id": 999})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ak", "as")
	taskID, err := c.SendWorkNotification(context.Background(), "tok", 123, "u123", "你好")
	if err != nil {
		t.Fatal(err)
	}
	if taskID != 999 {
		t.Fatalf("期望 task_id=999，实际 %d", taskID)
	}
}

// TestGetSendResult_真实响应已读 用的是 2026-09-09 真机验证时（真实钉钉
// 个人团队 + 真实手机号）拿到的原始响应原文——不是编的样例。这条测试
// 直接对应实现中真的踩出来过的 bug：最初以为响应里有一个
// "status"/"progress_in_percent" 字段，`status >= 2` 才算"已完成"；
// 真实响应根本没有这两个字段，零值恒为 0，永远判不成"已完成"，会让
// 延迟回查一直在退避循环里空转到轮次耗尽——真机测试第一次就复现了：
// 一条已经真实送达（钉钉 App 上收到了）的消息，在 notification_records
// 里却卡死在 ACCEPTED 不再往前推进。
func TestGetSendResult_真实响应已读(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("task_id") != "999" {
			t.Fatalf("期望 task_id=999，实际 %s", r.URL.Query().Get("task_id"))
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok","send_result":{"failed_user_id_list":[],"forbidden_list":[],"invalid_dept_id_list":[],"invalid_user_id_list":[],"read_user_id_list":["036632523163121146"],"unread_user_id_list":[]},"request_id":"16kcs7iwwcvj6"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ak", "as")
	result, err := c.GetSendResult(context.Background(), "tok", 123, 999, "036632523163121146")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Done || !result.Success {
		t.Fatalf("期望 Done=true Success=true（真实已读响应），实际 %+v", result)
	}
}

func TestGetSendResult_失败名单(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errcode": 0, "errmsg": "ok",
			"send_result": map[string]any{
				"read_user_id_list": []string{}, "unread_user_id_list": []string{},
				"failed_user_id_list": []string{"u123"},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ak", "as")
	result, err := c.GetSendResult(context.Background(), "tok", 123, 999, "u123")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Done || result.Success || result.Reason != "SEND_FAILED" {
		t.Fatalf("期望 Done=true Success=false Reason=SEND_FAILED，实际 %+v", result)
	}
}

func TestGetSendResult_目标用户尚未出现在任何名单里视为未完成(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errcode": 0, "errmsg": "ok",
			"send_result": map[string]any{
				"read_user_id_list": []string{}, "unread_user_id_list": []string{},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ak", "as")
	result, err := c.GetSendResult(context.Background(), "tok", 123, 999, "u123")
	if err != nil {
		t.Fatal(err)
	}
	if result.Done {
		t.Fatal("目标 userid 没出现在任何名单里，期望 Done=false（还在处理中，继续退避重试）")
	}
}
