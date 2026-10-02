// Package dingtalk 是钉钉开放平台 API 的最小客户端，只封装本组件要用的四个
// 接口：gettoken、手机号查 userid（getbymobile）、发工作通知
// （asyncsend_v2）、查发送结果（getsendresult）。这里不含任何"该不该发、
// 发给谁"的判断——适配器只翻译协议，业务判断归通知中心。
//
// 四个接口的路径、参数名与响应字段都对照过真实钉钉的响应（用一个真实的
// 钉钉团队应用调通过）。错误分类（classify.go）里只有两个分支见过真实
// 响应，其余按公开文档推断，各分支的注释写明是哪种。
package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
		return fmt.Errorf("调用钉钉 API 失败（网络层）: %w", withoutQuery(err, method, c.baseURL+path))
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

// withoutQuery 去掉网络层错误里的 query：Go 的 *url.Error 带完整 URL，而
// gettoken 的 query 是 appsecret、其余接口是 access_token，这些错误会进日志
// 和 GetChannelHealth 的 last_error。只留方法与路径，底层原因原样保留。
func withoutQuery(err error, method, u string) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s %s: %w", method, u, ue.Err)
	}
	return err
}

type getTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"` // 秒
}

// FetchAccessToken 申请一个新的 access_token（GET /gettoken?appkey=&appsecret=）。
// 不做频率限制：缓存与去重是 tokenmgr 的事，本层只管调这一下。
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
// ⚠️ 返回的 task_id 只代表钉钉受理了，不代表送达：真实结果要另调
// GetSendResult，所以投递分 ACCEPTED / CONFIRMED 两个阶段。
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

// SendResult 是 GetSendResult 针对某一个 userid 的结果。
//
// ⚠️ getsendresult 的真实响应里没有 status / progress_in_percent 这类"进度"
// 字段，而是把收件人分进几个名单：read_user_id_list、unread_user_id_list、
// failed_user_id_list、invalid_user_id_list、forbidden_list（注意不是
// forbidden_user_id_list）。所以"有没有结果"看这个 userid 出现在哪个名单：
// 已读 / 未读 = 成功，失败 / 无效 / 禁止 = 失败，哪个都不在 = 还在处理。
// 照着不存在的进度字段判"已完成"，零值恒为 0，回查永远判不成完成，已送达的
// 消息也会一直停在 ACCEPTED 直到轮数用完。
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
