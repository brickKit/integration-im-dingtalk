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

// TestClassifyError_应用未开通权限不可重试 用的是 2026-09-09 真机验证时
// 从真实钉钉服务器拿到的原始响应（个人测试团队 + 未开通
// qyapi_get_member_by_mobile 权限，调 topapi/v2/user/getbymobile 触发）——
// 不是编出来的样例。这条错误此前落进"区分不了"的默认可重试桶，会白白
// 重试 3 次才放弃；权限问题需要人去开发者后台点一次"申请权限"，重试
// 不会自愈，已改判不可重试。
func TestClassifyError_应用未开通权限不可重试(t *testing.T) {
	realMsg := `ding talk error[subcode=60011,submsg=应用尚未开通所需的权限：[qyapi_get_member_by_mobile]，点击链接申请并开通即可：https://open-dev.dingtalk.com/appscope/apply?content=dingaq7iiv6ps2sgn3rt%23qyapi_get_member_by_mobile, {requiredScopes=[qyapi_get_member_by_mobile]}]`
	retryable, code, evict := ClassifyError(&APIError{Code: 88, Msg: realMsg})
	if retryable {
		t.Fatal("应用未开通权限期望不可重试——这是真机验证过的真实响应，不是编的样例")
	}
	if evict {
		t.Fatal("权限问题不该清用户缓存——跟这个手机号本身无关")
	}
	if code != "DINGTALK_88" {
		t.Fatalf("期望 DINGTALK_88，实际 %q", code)
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
