package dingtalk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 这些测试用 httptest.Server 模拟钉钉开放平台的接口，验证本组件自己的请求
// 构造与响应解析。模拟的响应形状对照过真实钉钉的响应；带"真实响应"字样的
// 测试用的就是真实响应原文。

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

// TestGetSendResult_真实响应已读 用的是真实钉钉返回的原始响应原文（真实团队
// + 真实手机号），不是编的样例。它守的是：响应里没有 status /
// progress_in_percent 这类进度字段，"已完成"只能看收件人出现在哪个名单。
// 照着进度字段判，零值恒为 0，回查永远判不成完成——已经送达的消息会在通知
// 中心一直停在 ACCEPTED。
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

// 连不上钉钉时，Go 的网络错误会带上完整 URL（含 query）。gettoken 的 query
// 里是 appsecret，其余接口是 access_token：错误会进日志、进
// GetChannelHealth 的 last_error 返回给调用方，所以错误文本里不能有它们。
func TestClient_网络层错误不带出appsecret与access_token(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close() // 地址立刻不可连，触发网络层错误

	c := NewClient(base, "ak1", "secret-should-not-leak")
	_, _, err := c.FetchAccessToken(context.Background())
	if err == nil {
		t.Fatal("期望网络层错误")
	}
	if strings.Contains(err.Error(), "secret-should-not-leak") {
		t.Fatalf("gettoken 的网络层错误带出了 appsecret：%v", err)
	}

	_, err = c.GetUserIDByMobile(context.Background(), "token-should-not-leak", "13800000000")
	if err == nil {
		t.Fatal("期望网络层错误")
	}
	if strings.Contains(err.Error(), "token-should-not-leak") {
		t.Fatalf("getbymobile 的网络层错误带出了 access_token：%v", err)
	}
	if !strings.Contains(err.Error(), "/topapi/v2/user/getbymobile") {
		t.Fatalf("错误里应保留接口路径便于排障，实际：%v", err)
	}
}
