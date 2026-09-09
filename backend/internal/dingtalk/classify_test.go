package dingtalk

import (
	"errors"
	"testing"
)

func TestClassifyError_用户不存在不可重试且清缓存(t *testing.T) {
	retryable, code, evict := ClassifyError(&APIError{Code: 60121, Msg: "该手机号不存在"})
	if retryable {
		t.Fatal("用户不存在期望不可重试")
	}
	if !evict {
		t.Fatal("期望清掉手机号缓存")
	}
	if code == "" {
		t.Fatal("期望有一个非空的 error_code")
	}
}

func TestClassifyError_限流可重试(t *testing.T) {
	retryable, _, evict := ClassifyError(&APIError{Code: 90018, Msg: "系统繁忙，请稍后重试"})
	if !retryable {
		t.Fatal("系统繁忙期望可重试")
	}
	if evict {
		t.Fatal("系统繁忙不该清用户缓存")
	}
}

func TestClassifyError_AppSecret无效不可重试(t *testing.T) {
	retryable, _, _ := ClassifyError(&APIError{Code: 40001, Msg: "invalid appsecret"})
	if retryable {
		t.Fatal("AppSecret 无效期望不可重试——配置错了重试只会刷屏")
	}
}

func TestClassifyError_网络错误可重试(t *testing.T) {
	retryable, code, _ := ClassifyError(errors.New("dial tcp: connection refused"))
	if !retryable {
		t.Fatal("网络层错误期望可重试")
	}
	if code != "NETWORK_ERROR" {
		t.Fatalf("期望 NETWORK_ERROR，实际 %q", code)
	}
}

func TestClassifyError_未知错误默认可重试(t *testing.T) {
	// 区分不了的错误一律当可重试，但计入次数上限（设计原则同
	// infra-notification 待决问题 3 的既定结论）。
	retryable, _, _ := ClassifyError(&APIError{Code: 99999, Msg: "some brand new error nobody has seen"})
	if !retryable {
		t.Fatal("未知错误期望默认可重试")
	}
}

func TestIsTokenExpired(t *testing.T) {
	if !IsTokenExpired(&APIError{Code: 42001, Msg: "access_token已过期"}) {
		t.Fatal("期望识别为 token 过期")
	}
	if IsTokenExpired(&APIError{Code: 40001, Msg: "invalid appsecret"}) {
		t.Fatal("AppSecret 无效不该被误判成 token 过期")
	}
	if IsTokenExpired(errors.New("network error")) {
		t.Fatal("非 APIError 不该被判成 token 过期")
	}
}
