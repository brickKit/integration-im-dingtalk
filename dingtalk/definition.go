// Package dingtalk is the DingTalk channel member (assembly protocol 10, be-protocol chapter 20): the notification
// component's deliveries sent to DingTalk as interactive cards, DingTalk's callbacks turned into action events on its
// own stream, the principals' own DingTalk bindings. It never calls an owner component (D68).
package dingtalk

import (
	"embed"
	"io/fs"

	core "github.com/brickKit/integration-im-dingtalk/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/component"
	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Identity.
const (
	Version  = "3.0.4"
	GRPCPort = 9207
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrations() fs.FS {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	return sub
}

// Keys are the component's own configuration keys (docs/design.md ID05-ID07, ID10, ID12).
func Keys() []config.Key {
	return []config.Key{
		{Name: core.KeyAPIURL, Type: config.String, Default: "https://api.dingtalk.com"},
		{Name: core.KeyOAPIURL, Type: config.String, Default: "https://oapi.dingtalk.com"},
		{Name: core.KeyLoginURL, Type: config.String, Default: "https://login.dingtalk.com"},
		{Name: core.KeyAppKey, Type: config.String, Required: true},
		{Name: core.KeyAppSecret, Type: config.String, Required: true, Secret: true},
		{Name: core.KeyCardTemplate, Type: config.String, Required: true},
		{Name: core.KeyCallbackToken, Type: config.String, Required: true, Secret: true},
		{Name: core.KeyCallbackAESKey, Type: config.String, Required: true, Secret: true},
		{Name: core.KeyLinkBase, Type: config.String, Required: true},
		{Name: core.KeyOAuthRedirect, Type: config.String, Required: true},
		{Name: core.KeyBindReturn, Type: config.String, Required: true},
	}
}

// Own describes the own keys for the manifest's configSchema.
var Own = map[string]string{
	core.KeyAPIURL:         "DingTalk's API (the application's token, cards, OAuth): the simulator's address in a cell",
	core.KeyOAPIURL:        "DingTalk's old API (a union id's user)",
	core.KeyLoginURL:       "DingTalk's OAuth consent page",
	core.KeyAppKey:         "the DingTalk application's key (AppKey); also the callbacks' application id",
	core.KeyAppSecret:      "the DingTalk application's secret",
	core.KeyCardTemplate:   "the interactive card's template id",
	core.KeyCallbackToken:  "the callbacks' signature token",
	core.KeyCallbackAESKey: "the callbacks' encryption key (43 base64 characters)",
	core.KeyLinkBase:       "the front end's address the delivered links are relative to (https://erp.example.com)",
	core.KeyOAuthRedirect:  "the public address of GET /api/im-dingtalk/bindings/callback, as DingTalk's application registers it",
	core.KeyBindReturn:     "where the browser goes back to after binding (a front-end page; ?dingtalk=bound, taken or failed)",
}

// Definition is the component as the launcher and the shells take it.
func Definition() component.Definition {
	return component.Definition{ID: core.ID, Version: Version, Keys: Keys(), Migrations: migrations(), GRPCPort: GRPCPort,
		Streams: []events.Stream{{Name: core.Stream, Partitions: 4}}, Authz: core.Definitions(), Recovery: core.OpRecover,
		Resend: core.OpResend, ExternalCommands: true,
		New: func(rt *runtime.Runtime) (component.Module, error) { return New(rt) }}
}
