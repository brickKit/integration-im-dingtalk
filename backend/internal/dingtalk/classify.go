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
// ⚠️ 判据按 errmsg 文本关键词匹配，而不是 errcode 数字——这是"只做到
// 能编译通过的程度"这条既定决策的直接后果：没有真实钉钉沙盒可验证
// 具体错误码是不是真的对应这些场景（本文件顶部 dingtalk.md 设计计划
// §9 待决问题 1 明确要求"真调钉钉沙盒验证"，本阶段跳过了）。等有真实
// 测试企业时要用真实响应验证这里的关键词列表，届时大概率要改成更精确
// 的 errcode 判断。
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
