//go:build integration

package itest

// The DingTalk channel member against PostgreSQL and the bus over its real surfaces (L2). DingTalk is a fake the test
// swaps in (core.DingTalk: the cards it was sent, the OAuth codes it knows, what a send answers); the callbacks are
// signed and encrypted by the test as DingTalk would. The notification component is the test calling the system
// surface over mTLS as infra/notification. Needs BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL; the member listens on
// 28207 (user) and 9207 (system). Run: go test -tags integration ./itest/ (make test-integration); the index of the
// steps is itest/AGENTS.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brickKit/integration-im-dingtalk/v3/dingtalk"
	core "github.com/brickKit/integration-im-dingtalk/v3/internal/core"
	crypto "github.com/brickKit/integration-im-dingtalk/v3/internal/dingtalk"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/channelpb"
	"github.com/brickKit/be-sdk-go/v3/component"
	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/events/bus"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	itPort   = "28207"
	itBase   = "http://localhost:" + itPort
	itSystem = "localhost:9207"
	itSchema = "integration_im_dingtalk_it"
	appKey   = "ding-app-it"
	aesKey   = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	cbToken  = "cb-token-it"
	carol    = "0190f3a1-0000-7000-8000-0000000000c3" // binds her own DingTalk user
	dave     = "0190f3a1-0000-7000-8000-0000000000d4" // no grant at all
)

// fakeDingTalk is DingTalk as the component sees it: the cards sent to it, the OAuth codes it knows, what the next
// sends answer.
type fakeDingTalk struct {
	mu      sync.Mutex
	cards   []core.Card
	codes   map[string]string // OAuth code -> DingTalk user
	answers []string          // what each send answers in turn (succeeded when over)
}

func (f *fakeDingTalk) SendCard(_ context.Context, c core.Card) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cards = append(f.cards, c)
	class := core.SentOK
	if len(f.answers) > 0 {
		class, f.answers = f.answers[0], f.answers[1:]
	}
	return class, "fake"
}

func (f *fakeDingTalk) UserOfCode(_ context.Context, code string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.codes[code]
	if !ok {
		return "", fmt.Errorf("unknown code")
	}
	return u, nil
}

func (f *fakeDingTalk) ConsentURL(state string) string {
	return "https://login.example/oauth2/auth?" + url.Values{"state": {state}}.Encode()
}

func (f *fakeDingTalk) cardsTo(outTrack string) []core.Card {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Card
	for _, c := range f.cards {
		if c.OutTrackID == outTrack {
			out = append(out, c)
		}
	}
	return out
}

// fixture is the running member and the world, shared by the steps.
type fixture struct {
	t     *testing.T
	ctx   context.Context
	w     *sectest.World
	admin *pgx.Conn
	ding  *fakeDingTalk
	alice client // everything
	bob   client // binds his own user
	carol client
	dave  client
	chann channelpb.ChannelServiceClient // as infra/notification
	other channelpb.ChannelServiceClient // as another component
	mod   *dingtalk.Module
}

func TestDingTalkEndToEnd(t *testing.T) {
	fx := start(t)
	fx.bindings()
	fx.deliveries()
	fx.sending()
	fx.callbacks()
	fx.expiry()
	fx.purge()
}

func start(t *testing.T) *fixture {
	adminURL, natsURL := os.Getenv("BE_SDK_TEST_PG_ADMIN"), os.Getenv("BE_SDK_TEST_NATS_URL")
	if adminURL == "" || natsURL == "" {
		t.Fatal("BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL are needed")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	t.Cleanup(func() { admin.Close(ctx) })
	_, err = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+itSchema+" CASCADE; CREATE SCHEMA "+itSchema)
	require.NoError(t, err)
	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	pw, _ := u.User.Password()
	w := sectest.New(t)
	w.GrantAll(sectest.Alice, core.Definitions())
	for _, who := range []string{sectest.Bob, carol} {
		w.Grant(who, sdkauthz.Segment{ResourceType: core.MyBinding, Operation: core.OpBindSelf, Atoms: map[string]sdkauthz.Atom{},
			Valid: sdkauthz.Window{From: "2026-01-01T00:00:00Z"}})
	}
	fx := &fixture{t: t, ctx: ctx, w: w, admin: admin, ding: &fakeDingTalk{codes: map[string]string{
		"code-bob": "u-bob", "code-carol": "u-carol", "code-carol-2": "u-carol-2", "code-carol-as-bob": "u-bob"}}}
	portFree(t, itPort, itSystem)

	tr, err := bus.Open(ctx, natsURL, "dingtalk-it", slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { tr.Close() })
	require.NoError(t, tr.EnsureStream(ctx, events.Stream{Name: core.Stream, Partitions: 4}))

	env := w.Env(t, core.ID)
	for k, v := range map[string]string{"COMPONENT_ID": core.ID, "COMPONENT_VERSION": dingtalk.Version, "PORT": itPort,
		"STOP_DRAIN_DELAY": "50ms", "PG_HOST": u.Hostname(), "PG_PORT": u.Port(), "PG_DATABASE": strings.TrimPrefix(u.Path, "/"),
		"PG_USER": u.User.Username(), "PG_PASSWORD": pw, "PG_SCHEMA": itSchema, "PG_OWNER_USER": u.User.Username(),
		"PG_OWNER_PASSWORD": pw, "BUS_URL": natsURL, "IMPLEMENTATION_INSTANCE": ids.New().String(),
		core.KeyAppKey: appKey, core.KeyAppSecret: "secret", core.KeyCardTemplate: "tpl", core.KeyCallbackToken: cbToken,
		core.KeyCallbackAESKey: aesKey, core.KeyLinkBase: "https://erp.example", core.KeyOAuthRedirect: itBase +
			"/api/im-dingtalk/bindings/callback", core.KeyBindReturn: "https://erp.example/me"} {
		env[k] = v
	}
	var logs bytes.Buffer
	var logMu sync.Mutex
	t.Cleanup(func() {
		if t.Failed() {
			logMu.Lock()
			t.Log(logs.String())
			logMu.Unlock()
		}
	})
	out := &lockedWriter{w: &logs, mu: &logMu}
	opts := launcher.Options{Stdout: out, Stderr: out,
		Env: config.Env{LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }, Read: os.ReadFile}}
	migrate := opts
	migrate.Args = []string{"migrate"}
	def := dingtalk.Definition()
	newModule := def.New
	def.New = func(rt *runtime.Runtime) (component.Module, error) {
		m, err := newModule(rt)
		if err == nil {
			m.(*dingtalk.Module).UseClient(fx.ding)
			fx.mod = m.(*dingtalk.Module)
		}
		return m, err
	}
	require.Equal(t, launcher.ExitOK, launcher.Run(ctx, def, migrate))
	sctx, stop := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() { done <- launcher.Run(sctx, def, opts) }()
	t.Cleanup(func() { stop(); <-done })
	require.Eventually(t, func() bool {
		r, err := http.Get(itBase + "/readyz")
		if err == nil {
			r.Body.Close()
		}
		return err == nil && r.StatusCode == 200
	}, 20*time.Second, 100*time.Millisecond)
	fx.alice = client{t, w.Bearer(sectest.Alice)}
	fx.bob = client{t, w.Bearer(sectest.Bob)}
	fx.carol = client{t, w.Bearer(carol)}
	fx.dave = client{t, w.Bearer(dave)}
	fx.chann = channelpb.NewChannelServiceClient(fx.dial("infra/notification"))
	fx.other = channelpb.NewChannelServiceClient(fx.dial("erp/purchase"))
	return fx
}

// dial is a component's mTLS connection to the member's system surface.
func (fx *fixture) dial(as string) *grpc.ClientConn {
	conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(fx.w.TLS(fx.t, as).ClientConfig(core.ID))))
	require.NoError(fx.t, err)
	fx.t.Cleanup(func() { conn.Close() })
	return conn
}

// callback posts a DingTalk callback as DingTalk would: the event encrypted, the signature over it, a timestamp
// shifted by skew; answers the status and the decrypted answer.
func (fx *fixture) callback(ev map[string]any, skew time.Duration, token string) (int, string) {
	fx.t.Helper()
	plain, _ := json.Marshal(ev)
	enc, err := crypto.Encrypt(aesKey, appKey, plain)
	require.NoError(fx.t, err)
	ts := strconv.FormatInt(time.Now().Add(skew).UnixMilli(), 10)
	nonce := ids.New().String()[:8]
	q := url.Values{"signature": {crypto.Sign(token, ts, nonce, enc)}, "timestamp": {ts}, "nonce": {nonce}}
	body, _ := json.Marshal(map[string]string{"encrypt": enc})
	resp, err := http.Post(itBase+"/api/im-dingtalk/callback?"+q.Encode(), "application/json", bytes.NewReader(body))
	require.NoError(fx.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var ans struct {
		Encrypt string `json:"encrypt"`
	}
	if json.Unmarshal(raw, &ans) == nil && ans.Encrypt != "" {
		msg, err := crypto.Decrypt(aesKey, appKey, ans.Encrypt)
		require.NoError(fx.t, err)
		return resp.StatusCode, string(msg)
	}
	return resp.StatusCode, ""
}

// outbox are the payloads of the events of one type the member emitted.
func (fx *fixture) outbox(typ string) []map[string]any {
	fx.t.Helper()
	rows, err := fx.admin.Query(fx.ctx, "SELECT convert_from(payload, 'UTF8') FROM "+itSchema+".be_outbox WHERE type = $1 "+
		"ORDER BY seq NULLS LAST", typ)
	require.NoError(fx.t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var s string
		require.NoError(fx.t, rows.Scan(&s))
		var m map[string]any
		require.NoError(fx.t, json.Unmarshal([]byte(s), &m))
		out = append(out, m)
	}
	return out
}

// --- helpers ---

type client struct {
	t     *testing.T
	token string
}

func (c client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, itBase+path, r)
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// portFree fails at once when a port the member needs is taken (a running cell or another member would answer).
func portFree(t *testing.T, addrs ...string) {
	for _, a := range addrs {
		if !strings.Contains(a, ":") {
			a = "localhost:" + a
		}
		ln, err := net.Listen("tcp", a)
		require.NoError(t, err, fmt.Sprintf("port %s is taken: is a cell or another member running?", a))
		_ = ln.Close()
	}
}
