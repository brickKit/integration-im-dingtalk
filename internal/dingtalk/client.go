// Package dingtalk is DingTalk as this component uses it (docs/design.md ID02, ID03, ID05, ID10): the application's
// access token, cached; an interactive card sent to one user; an OAuth authorization code exchanged for the user it
// was given to; the consent page; the callbacks' signature and encryption (crypto.go). Requests go through the SDK's
// outbound calls. The interface is modelled on DingTalk's public documents as of 2026-11 and to be checked against its
// test organisation before a real deployment (the design's open questions).
package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	core "github.com/brickKit/integration-im-dingtalk/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/errs"
)

// Client is DingTalk for one application.
type Client struct {
	API, OAPI *calls.HTTP // the new API (api.dingtalk.com) and the old one (oapi.dingtalk.com)
	Login     string      // the consent page's base
	Cfg       core.Config

	mu      sync.Mutex
	token   string
	expires time.Time
}

var _ core.DingTalk = (*Client)(nil)

// accessToken is the application's token, cached until five minutes before it expires (ID05). The secret goes in the
// request's body, never in a URL (ID12), and a failure answers without the transport's detail.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	var out struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int    `json:"expireIn"`
	}
	err := c.API.Do(ctx, calls.Request{Method: "POST", Path: "/v1.0/oauth2/accessToken",
		Body: map[string]string{"appKey": c.Cfg.AppKey, "appSecret": c.Cfg.AppSecret()}}, &out)
	var se *calls.StatusError
	switch {
	case errors.As(err, &se) && se.Status >= 400 && se.Status < 500:
		return "", errs.New(errs.ReasonDependencyUnavailable, "DingTalk refused the application's credentials")
	case err != nil:
		return "", errs.New(errs.ReasonDependencyUnavailable, "DingTalk did not give the application's token")
	case out.AccessToken == "":
		return "", errs.New(errs.ReasonDependencyUnavailable, "DingTalk refused the application's credentials")
	}
	c.token, c.expires = out.AccessToken, time.Now().Add(time.Duration(out.ExpireIn)*time.Second-5*time.Minute)
	return c.token, nil
}

func (c *Client) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
}

// SendCard sends one interactive card (ID03): accepted is sent; a connection that failed before sending, a rate
// limit and a refused token are not reached (sent again later; the token fetched anew); a timeout and a 5xx may have
// reached; any other 4xx is refused for good.
func (c *Client) SendCard(ctx context.Context, card core.Card) (string, string) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return core.NotReached, "no access token"
	}
	buttons, _ := json.Marshal(card.Buttons)
	body := map[string]any{"cardTemplateId": c.Cfg.CardTemplate, "outTrackId": card.OutTrackID,
		"receiverUserIdList": []string{card.UserID}, "callbackRouteKey": "be",
		"cardData": map[string]any{"cardParamMap": map[string]string{"title": PlainText(card.Title), "body": PlainText(card.Body),
			"buttons": string(buttons)}}}
	var out struct {
		Success bool `json:"success"`
	}
	err = c.API.Do(ctx, calls.Request{Method: "POST", Path: "/v1.0/im/interactiveCards/send", Body: body,
		Headers: map[string]string{"x-acs-dingtalk-access-token": token}}, &out)
	var se *calls.StatusError
	switch {
	case err == nil && out.Success:
		return core.SentOK, "accepted"
	case err == nil:
		return core.RejectedClass, "not accepted"
	case errors.Is(err, calls.ErrNotSent):
		return core.NotReached, "the connection failed before sending"
	case errors.As(err, &se) && se.Status == 429:
		return core.NotReached, "rate limited"
	case errors.As(err, &se) && (se.Status == 401 || se.Status == 403):
		c.forget()
		return core.NotReached, "the access token was refused"
	case errors.As(err, &se) && se.Status >= 400 && se.Status < 500:
		return core.RejectedClass, "refused: " + http4xx(se.Body)
	}
	return core.MaybeReached, "no answer"
}

// PlainText is a value as plain text in a card (ID02): every character DingTalk's card Markdown interprets is escaped
// with a backslash, and line breaks and other control characters become spaces.
func PlainText(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteRune(' ')
		case strings.ContainsRune("\\`*_{}[]()<>#+-.!|~", r):
			b.WriteRune('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// http4xx is DingTalk's error code of a refusal (never the body as it is).
func http4xx(body []byte) string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Code == "" {
		return "unknown"
	}
	return e.Code
}

// UserOfCode exchanges an OAuth code for the user it was given to (ID10): the user's own access token, the user's
// union id, then the organisation's user id.
func (c *Client) UserOfCode(ctx context.Context, code string) (string, error) {
	var tok struct {
		AccessToken string `json:"accessToken"`
	}
	err := c.API.Do(ctx, calls.Request{Method: "POST", Path: "/v1.0/oauth2/userAccessToken", Body: map[string]string{
		"clientId": c.Cfg.AppKey, "clientSecret": c.Cfg.AppSecret(), "code": code, "grantType": "authorization_code"}}, &tok)
	var se *calls.StatusError
	switch {
	case errors.As(err, &se) && se.Status >= 400 && se.Status < 500, err == nil && tok.AccessToken == "":
		return "", errs.New(errs.ReasonRequestInvalid, "DingTalk did not accept the authorization")
	case err != nil:
		return "", errs.New(errs.ReasonDependencyUnavailable, "DingTalk did not answer the authorization")
	}
	var me struct {
		UnionID string `json:"unionId"`
	}
	if err := c.API.Do(ctx, calls.Request{Method: "GET", Path: "/v1.0/contact/users/me",
		Headers: map[string]string{"x-acs-dingtalk-access-token": tok.AccessToken}}, &me); err != nil || me.UnionID == "" {
		return "", errs.New(errs.ReasonDependencyUnavailable, "DingTalk did not answer who the user is")
	}
	for attempt := 0; ; attempt++ {
		app, err := c.accessToken(ctx)
		if err != nil {
			return "", err
		}
		var u struct {
			ErrCode int `json:"errcode"`
			Result  struct {
				UserID string `json:"userid"`
			} `json:"result"`
		}
		if err := c.OAPI.Do(ctx, calls.Request{Method: "POST", Path: "/topapi/user/getbyunionid?" +
			url.Values{"access_token": {app}}.Encode(), Body: map[string]string{"unionid": me.UnionID}, Idempotent: true},
			&u); err != nil {
			return "", errs.New(errs.ReasonDependencyUnavailable, "DingTalk did not answer the user of a union id")
		}
		if tokenRefused(u.ErrCode) && attempt == 0 { // invalidated before it expired: fetched again, once (ID05)
			c.forget()
			continue
		}
		if u.ErrCode != 0 || u.Result.UserID == "" {
			return "", errs.New(errs.ReasonRequestInvalid, "the DingTalk user is not in the organisation")
		}
		return u.Result.UserID, nil
	}
}

// tokenRefused is the old API's answer to an application token it no longer takes: invalid (40014) or expired (42001).
func tokenRefused(code int) bool { return code == 40014 || code == 42001 }

// ConsentURL is DingTalk's consent page for a binding's state (ID10).
func (c *Client) ConsentURL(state string) string {
	q := url.Values{"redirect_uri": {c.Cfg.OAuthRedirect}, "response_type": {"code"}, "client_id": {c.Cfg.AppKey},
		"scope": {"openid"}, "state": {state}, "prompt": {"consent"}}
	return c.Login + "/oauth2/auth?" + q.Encode()
}
