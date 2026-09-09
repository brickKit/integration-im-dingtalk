// Package dingtalk 是钉钉开放平台 API 的最小客户端——只封装本组件需要
// 的四个接口（获取 access_token、手机号查 userid、发工作通知、查发送
// 结果）。⚠️ 这里不含任何"该不该发"、"发给谁"的判断——那是 consumer 包
// 的事，本包只管"怎么调这四个 HTTP 接口"（设计计划 §6.9：适配器只调
// 外部 API，严禁业务逻辑）。
//
// ⚠️⚠️ 阶段三 Task 9 建仓库时没有可用的钉钉沙盒/测试企业——用户明确决定
// 跳过真机验证，只做到"能编译通过、内部逻辑用 httptest 模拟钉钉的接口
// 形状验证过"的程度（03-阶段三 Task 9 小节有完整记录）。这意味着：
// URL 路径、参数名、JSON 字段名是按钉钉开放平台公开文档的既有形状写的，
// 但从未打到真实的钉钉服务器验证过。等有真实测试企业凭据后需要补一次
// 真机验证，届时这里大概率要修正字段名或错误码判断。
package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	httpClient *http.Client
	baseURL    string
	appKey     string
	appSecret  string
}

func NewClient(baseURL, appKey, appSecret string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    baseURL, appKey: appKey, appSecret: appSecret,
	}
}

// APIError 是钉钉"HTTP 200 但 errcode != 0"的那一类错误——钉钉开放平台
// 的通用约定：只有真正连不上/超时才是 Go 的网络错误，业务失败都装在
// 200 响应体里。
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("钉钉 API 错误 errcode=%d: %s", e.Code, e.Msg)
}

type apiEnvelope struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// doJSON 是四个接口共用的请求/响应骨架：GET 或 POST，JSON body（GET 时
// body 传 nil），解出 errcode/errmsg，非零则包成 *APIError。
func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("编码请求体: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return fmt.Errorf("建请求: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("调用钉钉 API 失败（网络层）: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读钉钉响应体: %w", err)
	}

	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("解析钉钉响应体: %w（原文: %s）", err, string(raw))
	}
	if env.ErrCode != 0 {
		return &APIError{Code: env.ErrCode, Msg: env.ErrMsg}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("解析钉钉响应体: %w（原文: %s）", err, string(raw))
		}
	}
	return nil
}

type getTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"` // 秒
}

// FetchAccessToken 申请一个新的 access_token（钉钉开放平台
// GET /gettoken?appkey=&appsecret=）。⚠️ 不做频率限制——那是调用方
// （service/consumer 层的 token 缓存策略）的责任，本层只管"调这一下"。
func (c *Client) FetchAccessToken(ctx context.Context) (token string, expiresIn time.Duration, err error) {
	q := url.Values{"appkey": {c.appKey}, "appsecret": {c.appSecret}}
	var resp getTokenResponse
	if err := c.doJSON(ctx, http.MethodGet, "/gettoken", q, nil, &resp); err != nil {
		return "", 0, err
	}
	return resp.AccessToken, time.Duration(resp.ExpiresIn) * time.Second, nil
}

type getByMobileResponse struct {
	Result struct {
		Userid string `json:"userid"`
	} `json:"result"`
}

// GetUserIDByMobile 按手机号查钉钉 userid（POST
// /topapi/v2/user/getbymobile?access_token=）。
func (c *Client) GetUserIDByMobile(ctx context.Context, accessToken, mobile string) (string, error) {
	q := url.Values{"access_token": {accessToken}}
	var resp getByMobileResponse
	if err := c.doJSON(ctx, http.MethodPost, "/topapi/v2/user/getbymobile", q,
		map[string]string{"mobile": mobile}, &resp); err != nil {
		return "", err
	}
	return resp.Result.Userid, nil
}

type asyncSendResponse struct {
	TaskID int64 `json:"task_id"`
}

// SendWorkNotification 发一条文本工作通知给单个 userid（POST
// /topapi/message/corpconversation/asyncsend_v2?access_token=）。
// ⚠️ 返回的 task_id 只代表"钉钉受理了"，不代表送达（设计计划 §4.2）——
// 真实结果要另调 GetSendResult。
func (c *Client) SendWorkNotification(ctx context.Context, accessToken string, agentID int64, userid, content string) (int64, error) {
	q := url.Values{"access_token": {accessToken}}
	body := map[string]any{
		"agent_id":    agentID,
		"userid_list": userid,
		"msg": map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": content},
		},
	}
	var resp asyncSendResponse
	if err := c.doJSON(ctx, http.MethodPost, "/topapi/message/corpconversation/asyncsend_v2", q, body, &resp); err != nil {
		return 0, err
	}
	return resp.TaskID, nil
}

// SendResult 是 GetSendResult 针对某一个 userid 的结果（设计计划 §4.2
// 第②步）。
//
// ⚠️ 真机验证过（2026-09-09，真实钉钉个人团队）：这里最初假设响应里有
// 一个 "status"/"progress_in_percent" 字段（0.1 版没有沙盒时照着钉钉
// 文档某个版本的旧描述猜的），而真实响应根本没有这两个字段，是直接把
// 收件人分类进几个名单（`read_user_id_list`/`unread_user_id_list`/
// `failed_user_id_list`/`invalid_user_id_list`/`forbidden_list`——
// 后一个字段名也不是猜测的 `forbidden_user_id_list`）。原先按
// `status >= 2` 判"是否已完成"，字段不存在时零值恒为 0，永远判不成
// "已完成"，会一直在延迟回查的退避循环里空转到轮次耗尽——这是一个真实
// 会导致所有投递永远停在 ACCEPTED 直到超时的 bug，靠真实响应验证才
// 发现。现在按"这个 userid 有没有出现在任意一个名单里"判断"是否已经
// 有结果"：出现在已读/未读名单 = 成功，出现在失败/无效/禁止名单 =
// 失败，哪个名单都没出现 = 还在处理中。
type SendResult struct {
	Done    bool   // 这个 userid 有没有出现在任意一个名单里
	Success bool   // 只有 Done=true 时有意义
	Reason  string // 只有 Done=true 且 Success=false 时有意义："SEND_FAILED"/"INVALID_USER"/"FORBIDDEN_USER"
}

type getSendResultResponse struct {
	SendResult struct {
		ReadUserIDList    []string `json:"read_user_id_list"`
		UnreadUserIDList  []string `json:"unread_user_id_list"`
		FailedUserIDList  []string `json:"failed_user_id_list"`
		InvalidUserIDList []string `json:"invalid_user_id_list"`
		ForbiddenList     []string `json:"forbidden_list"`
	} `json:"send_result"`
}

func containsUserID(list []string, userid string) bool {
	for _, u := range list {
		if u == userid {
			return true
		}
	}
	return false
}

// GetSendResult 查一个 task_id 对某个 userid 的真实投递结果（GET
// /topapi/message/corpconversation/getsendresult?access_token=&agent_id=&task_id=）。
// ⚠️ 这个接口是按整个 task 查全部收件人分类名单，不是按 userid 查单个
// 结果——本组件目前 `SendWorkNotification` 每次只发给一个 userid，所以
// 这里直接在返回的名单里找这一个 userid 归到了哪一类；如果以后要支持
// 一次发给多个 userid，这个函数要改成返回全量分类结果，不能再只认
// 一个 userid。
func (c *Client) GetSendResult(ctx context.Context, accessToken string, agentID, taskID int64, userid string) (*SendResult, error) {
	q := url.Values{
		"access_token": {accessToken},
		"agent_id":     {strconv.FormatInt(agentID, 10)},
		"task_id":      {strconv.FormatInt(taskID, 10)},
	}
	var resp getSendResultResponse
	if err := c.doJSON(ctx, http.MethodGet, "/topapi/message/corpconversation/getsendresult", q, nil, &resp); err != nil {
		return nil, err
	}
	sr := resp.SendResult
	switch {
	case containsUserID(sr.ReadUserIDList, userid), containsUserID(sr.UnreadUserIDList, userid):
		return &SendResult{Done: true, Success: true}, nil
	case containsUserID(sr.FailedUserIDList, userid):
		return &SendResult{Done: true, Success: false, Reason: "SEND_FAILED"}, nil
	case containsUserID(sr.InvalidUserIDList, userid):
		return &SendResult{Done: true, Success: false, Reason: "INVALID_USER"}, nil
	case containsUserID(sr.ForbiddenList, userid):
		return &SendResult{Done: true, Success: false, Reason: "FORBIDDEN_USER"}, nil
	default:
		return &SendResult{Done: false}, nil
	}
}
