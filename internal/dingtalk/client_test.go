package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	core "github.com/brickKit/integration-im-dingtalk/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DingTalk invalidates an application's token before it expires (its secret reset, a second token issued): the old
// API answers 40014 or 42001. The cached token is dropped and fetched again, and the call made once more (ID05), as a
// card's 401 already does; without it a binding fails as "not in the organisation" until the cache runs out.
func TestAnInvalidatedTokenIsFetchedAgain(t *testing.T) {
	issued, valid := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/gettoken":
			issued++
			valid = fmt.Sprintf("t%d", issued)
			answer(map[string]any{"errcode": 0, "access_token": valid, "expires_in": 7200})
		case "/v1.0/oauth2/userAccessToken":
			answer(map[string]any{"accessToken": "u"})
		case "/v1.0/contact/users/me":
			answer(map[string]any{"unionId": "union-1"})
		case "/topapi/user/getbyunionid":
			if r.URL.Query().Get("access_token") != valid {
				answer(map[string]any{"errcode": 40014, "errmsg": "invalid access_token"})
				return
			}
			answer(map[string]any{"errcode": 0, "result": map[string]any{"userid": "dt-1"}})
		}
	}))
	defer srv.Close()
	m, err := calls.NewMember(nil)
	require.NoError(t, err)
	c := &Client{API: m.HTTP("api", srv.URL), OAPI: m.HTTP("oapi", srv.URL),
		Cfg: core.Config{AppKey: "k", AppSecret: func() string { return "s" }}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.token, c.expires = "stale", time.Now().Add(time.Hour) // cached, invalidated by DingTalk meanwhile
	user, err := c.UserOfCode(ctx, "code")
	require.NoError(t, err)
	assert.Equal(t, "dt-1", user)
	assert.Equal(t, 1, issued, "fetched once again")
}
