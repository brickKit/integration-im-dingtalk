package dingtalk

import (
	"errors"
	"strconv"
	"strings"
)

// ClassifyError 回答"这个错误值不值得重试"。这件事只有适配器知道（错误码是
// 钉钉特有的）；要不要真的重试、隔多久、重试几次归通知中心。两边都判会
// 互相打架，都不判就会给离职员工重试好几次。
//
// ⚠️ 判据按 errmsg 的关键词匹配，不按 errcode 数字。见过真实响应的只有
// "应用未开通所需权限"一类（errcode=88，sub_msg 形如"应用尚未开通所需的
// 权限：[qyapi_get_member_by_mobile]…"）：这类错误要人去开发者后台开通
// 权限，重试不会自愈，判不可重试。其余分支按公开文档推断。
//
// evictUserCache=true 时调用方要删掉 dingtalk_user_map 里这个手机号的缓存
// 行（缓存的过期策略）。
func ClassifyError(err error) (retryable bool, errorCode string, evictUserCache bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		// 网络层错误（超时、连接失败）——外部临时故障，值得重试。
		return true, "NETWORK_ERROR", false
	}

	msg := apiErr.Msg
	switch {
	case containsAny(msg, "不存在", "不在企业", "手机号未绑定", "查询不到"):
		// 这个人在钉钉那边找不到，重试多少次都一样；同时清掉缓存的 userid
		// 映射，人换了手机号或重新入职后能重新解析。（推断）
		return false, apiErrorCode(apiErr), true
	case containsAny(msg, "AppKey", "AppSecret", "appkey", "appsecret", "无效的access_token", "access_token不合法"):
		// 配置错了，重试只会刷屏，要让运维看见。access_token 过期不走到这里：
		// consumer 先识别它（IsTokenExpired）、自己刷新后重试一次。（推断）
		return false, apiErrorCode(apiErr), false
	case containsAny(msg, "未开通所需的权限", "尚未开通", "无权限", "no privilege", "forbidden"):
		// 见过真实响应（errcode=88，见本函数顶部注释）：应用没开通需要的
		// 权限，要人去开发者后台开通，重试不会自愈。
		return false, apiErrorCode(apiErr), false
	case containsAny(msg, "系统繁忙", "频率", "限流", "超过调用限制", "concurrency"):
		return true, apiErrorCode(apiErr), false
	default:
		// 区分不了的错误一律当可重试；重试次数的上限在通知中心。
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

// IsTokenExpired 判断这个错误是不是"access_token 失效 / 过期"：这一类由本
// 组件自己刷新后重试一次再报，不消耗通知中心的重试次数。
func IsTokenExpired(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return containsAny(apiErr.Msg, "access_token", "token不存在", "token已过期", "token expired") &&
		!containsAny(apiErr.Msg, "AppKey", "AppSecret")
}
