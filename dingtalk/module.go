package dingtalk

import (
	"context"
	"time"

	"github.com/brickKit/integration-im-dingtalk/v3/internal/bindings"
	"github.com/brickKit/integration-im-dingtalk/v3/internal/callbacks"
	core "github.com/brickKit/integration-im-dingtalk/v3/internal/core"
	"github.com/brickKit/integration-im-dingtalk/v3/internal/deliveries"
	client "github.com/brickKit/integration-im-dingtalk/v3/internal/dingtalk"

	"github.com/brickKit/be-sdk-go/v3/channelpb"
	"github.com/brickKit/be-sdk-go/v3/extcmd"
	"github.com/brickKit/be-sdk-go/v3/grpcx"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	"github.com/brickKit/be-sdk-go/v3/work"
)

// Module is the DingTalk channel member as the launcher and the shells run it: it only assembles the business modules
// under internal/ (their map is internal/AGENTS.md).
type Module struct {
	channelpb.UnimplementedChannelServiceServer
	b        *core.Base
	counters *callbacks.Counters
}

// New builds the module from its runtime: the configuration, DingTalk's client over the SDK's outbound calls, the
// external commands' settle hook (the result fact).
func New(rt *runtime.Runtime) (*Module, error) {
	v := rt.Config
	cfg := core.Config{APIURL: v.String(core.KeyAPIURL), OAPIURL: v.String(core.KeyOAPIURL), LoginURL: v.String(core.KeyLoginURL),
		AppKey: v.String(core.KeyAppKey), CardTemplate: v.String(core.KeyCardTemplate),
		AppSecret: func() string { return v.String(core.KeyAppSecret) }, CallbackToken: func() string { return v.String(core.KeyCallbackToken) },
		AESKey: func() string { return v.String(core.KeyCallbackAESKey) }, LinkBase: v.String(core.KeyLinkBase),
		OAuthRedirect: v.String(core.KeyOAuthRedirect), BindReturn: v.String(core.KeyBindReturn)}
	b := &core.Base{RT: rt, Cfg: cfg}
	api, _ := rt.HTTPAt("dingtalk-api", cfg.APIURL)
	oapi, _ := rt.HTTPAt("dingtalk-oapi", cfg.OAPIURL)
	b.Client = &client.Client{API: api, OAPI: oapi, Login: cfg.LoginURL, Cfg: cfg}
	if rt.External != nil {
		rt.External.OnSettled = deliveries.Settled(b)
	}
	return &Module{b: b, counters: callbacks.NewCounters(rt.Metrics)}, nil
}

// UseClient replaces DingTalk's client (the integration tests: a fake DingTalk).
func (m *Module) UseClient(c core.DingTalk) { m.b.Client = c }

// ExternalCommands is the adapter that sends the cards and its capabilities (be-protocol chapter 13; ID03, ID11).
func (m *Module) ExternalCommands() (extcmd.Adapter, map[string]extcmd.Capabilities) {
	return deliveries.Adapter{M: m.b}, deliveries.Capabilities()
}

// Routes registers the user surface (contract/openapi.yaml): the callback and the binding's return are public (their
// own signature and state); the rest are the caller's own binding and the administrators'.
func (m *Module) Routes(r *httpx.Routes) error {
	h := func(f func(*core.Base, *httpx.Context) error) httpx.Handler {
		return func(c *httpx.Context) error { return f(m.b, c) }
	}
	r.Add(httpx.Route{Method: "POST", Path: "/api/im-dingtalk/callback", Public: true,
		Handle: func(c *httpx.Context) error { return callbacks.Receive(m.b, m.counters, c) }})
	r.Add(httpx.Route{Method: "GET", Path: "/api/im-dingtalk/bindings/callback", Public: true, Handle: h(bindings.Callback)})
	r.Add(httpx.Route{Method: "POST", Path: "/api/im-dingtalk/bindings/start", Operation: core.OpBindSelf, Handle: h(bindings.Start)})
	r.Add(httpx.Route{Method: "GET", Path: "/api/im-dingtalk/bindings/me", Operation: core.OpBindSelf, Handle: h(bindings.Mine)})
	r.Add(httpx.Route{Method: "DELETE", Path: "/api/im-dingtalk/bindings/me", Operation: core.OpBindSelf, Handle: h(bindings.UnbindMine)})
	r.Add(httpx.Route{Method: "GET", Path: "/api/im-dingtalk/bindings", Operation: core.OpBindRead, Handle: h(bindings.List)})
	r.Add(httpx.Route{Method: "DELETE", Path: "/api/im-dingtalk/bindings/:principal", Operation: core.OpBindEnd, Handle: h(bindings.End)})
	r.Add(httpx.Route{Method: "GET", Path: "/api/im-dingtalk/callbacks", Operation: core.OpCallbackRead, Handle: h(callbacks.Quarantined)})
	return nil
}

// Services registers be.channel.v1.ChannelService for the notification component (P20.1).
func (m *Module) Services(s *grpcx.Services) error {
	s.Register(&channelpb.ChannelService_ServiceDesc, m, map[string]grpcx.Method{
		"Deliver": {Operation: core.OpDeliver, Callers: []string{"infra/notification"}}})
	return nil
}

// Deliver is ChannelService.Deliver (ID01).
func (m *Module) Deliver(ctx context.Context, in *channelpb.DeliverRequest) (*channelpb.DeliverResponse, error) {
	return deliveries.Deliver(m.b, ctx, in)
}

// Jobs declares the background work (protocol 01): each received callback's processing, and the alert on the stuck.
func (m *Module) Jobs(j *work.Jobs) error {
	j.Add(work.Job{Name: callbacks.JobProcess, Kind: work.Queue, Effect: work.Local, MaxAttempts: work.Unlimited,
		Handle: func(ctx context.Context, r *work.Run) error { return callbacks.Process(m.b, ctx, r) }})
	j.Add(work.Job{Name: "callbacks-stuck", Kind: work.Singleton, Effect: work.Local, Interval: time.Minute,
		MaxAttempts: work.Unlimited, Handle: func(ctx context.Context, r *work.Run) error { return callbacks.Stuck(m.b, ctx, r) }})
	return nil
}
