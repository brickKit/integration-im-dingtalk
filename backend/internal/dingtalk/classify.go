package dingtalk

import (
	"errors"
	"strconv"
	"strings"
)

// ClassifyError 是设计计划 §4.1 那张分工表的代码落点："这个错误值不值得
// 重试"只有本适配器知道，infra-notification 只管"要不要真的重试、隔多久"
// （两边分工不能混，都判会打架，都不判会给离职员工重试 5 次）。
//
// ⚠️ 判据按 errmsg 文本关键词匹配，而不是 errcode 数字——早期没有真实
// 钉钉沙盒时留的判据。2026-09-09 用真实测试企业验证过一轮："应用未开通
// 所需权限"这一类（errcode=88，真实响应：`{"errcode":88,"sub_code":
// "60011","sub_msg":"应用尚未开通所需的权限：[qyapi_get_member_by_mobile]…"}`）
// 之前落进"区分不了"的默认可重试桶，会白白重试 3 次再放弃——这类错误
// 需要人去开发者后台点一次"申请权限"，重试不会让它自己好，已改判不可
// 重试。其余分支仍未用真实响应验证，见各自注释。
//
// evictUserCache=true 时调用方（consumer 包）要删掉 dingtalk_user_map
// 里对应手机号的缓存行（设计计划 §2 的过期策略）。
func ClassifyError(err error) (retryable bool, errorCode string, evictUserCache bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		// 网络层错误（超时、连接失败）——外部临时故障，值得重试。
		return true, "NETWORK_ERROR", false
	}

	msg := apiErr.Msg
	switch {
	case containsAny(msg, "不存在", "不在企业", "手机号未绑定", "查询不到"):
		// 设计计划 §4.1：这个人在钉钉那边找不到，重试一万次也一样，
		// 同时要清掉缓存的 userid 映射（下次换个手机号能重新解析）。
		return false, apiErrorCode(apiErr), true
	case containsAny(msg, "AppKey", "AppSecret", "appkey", "appsecret", "无效的access_token", "access_token不合法"):
		// 配置错了，重试只会刷屏，要让运维看见（除了 access_token 过期
		// 这种"我先自己刷一次再报"的情况——那个由 client 调用方在
		// SendWorkNotificationWithAutoRefresh 里拦截，不会走到这里）。
		return false, apiErrorCode(apiErr), false
	case containsAny(msg, "未开通所需的权限", "尚未开通", "无权限", "no privilege", "forbidden"):
		// ✅ 真机验证过（errcode=88，见本函数顶部注释）：应用没有申请到
		// 需要的 scope，需要人去开发者后台点一次"申请并开通"——同配置
		// 错误一样，重试不会自愈，值得让运维立刻看见而不是刷三次屏。
		return false, apiErrorCode(apiErr), false
	case containsAny(msg, "系统繁忙", "频率", "限流", "超过调用限制", "concurrency"):
		return true, apiErrorCode(apiErr), false
	default:
		// 区分不了的错误一律当可重试，但计入次数上限（同
		// infra-notification 设计计划 §9 待决问题 3 的既定原则）。
		return true, apiErrorCode(apiErr), false
	}
}

func apiErrorCode(e *APIError) string {
	return "DINGTALK_" + strconv.Itoa(e.Code)
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// IsTokenExpired 判断这个错误是不是"access_token 失效/过期"——这一类
// 由客户端自己刷新重试一次再报，不消耗 infra-notification 的重试次数
// （设计计划 §4.1 表格最后一行）。
func IsTokenExpired(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return containsAny(apiErr.Msg, "access_token", "token不存在", "token已过期", "token expired") &&
		!containsAny(apiErr.Msg, "AppKey", "AppSecret")
}
