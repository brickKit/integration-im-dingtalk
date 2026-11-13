// Package core is what every module of the DingTalk channel member shares: its names, the resources and operations,
// the configuration keys, the Base and the small helpers of the routes.
package core

import (
	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
)

// Identity and stream.
const (
	ID     = "integration/im-dingtalk"
	Stream = "integration.im_dingtalk" // a "-" in the name becomes "_" (be-protocol P18.2)
)

// The configuration keys (docs/design.md ID03, ID05-ID07, ID10, ID12).
const (
	KeyAPIURL         = "DINGTALK_API_URL"   // the new API (the application's token, cards, OAuth, the user): default https://api.dingtalk.com
	KeyOAPIURL        = "DINGTALK_OAPI_URL"  // the old API (union id to user id): default https://oapi.dingtalk.com
	KeyLoginURL       = "DINGTALK_LOGIN_URL" // the OAuth consent page: default https://login.dingtalk.com
	KeyAppKey         = "DINGTALK_APP_KEY"
	KeyAppSecret      = "DINGTALK_APP_SECRET"
	KeyCardTemplate   = "DINGTALK_CARD_TEMPLATE_ID"
	KeyCallbackToken  = "DINGTALK_CALLBACK_TOKEN"
	KeyCallbackAESKey = "DINGTALK_CALLBACK_AES_KEY"
	KeyLinkBase       = "DINGTALK_LINK_BASE"      // the front end's address the links are relative to
	KeyOAuthRedirect  = "DINGTALK_OAUTH_REDIRECT" // the public address of the binding callback route
	KeyBindReturn     = "DINGTALK_BIND_RETURN"    // where the browser goes back to after binding
)

// The resources and operations (docs/design.md, the commands and queries).
const (
	MyBinding      = "integration.im_dingtalk.my_binding"
	OpBindSelf     = "integration.im_dingtalk.my_binding.manage" // every user: one's own binding
	Bindings       = "integration.im_dingtalk.binding"
	OpBindRead     = "integration.im_dingtalk.binding.read"
	OpBindEnd      = "integration.im_dingtalk.binding.end"
	Callbacks      = "integration.im_dingtalk.callback"
	OpCallbackRead = "integration.im_dingtalk.callback.read"
	System         = "integration.im_dingtalk.system"
	OpDeliver      = "integration.im_dingtalk.system.deliver" // the notification component
	OpRecover      = "integration.im_dingtalk.system.recover"
	OpResend       = "integration.im_dingtalk.system.resend"
)

// SendCard is the external command's operation (ID03).
const SendCard = "send_card"

// Definitions are the component's resources and operations: none has a dimension (a binding is the caller's own, the
// rest are an administrator's or a service's).
func Definitions() *sdkauthz.Definitions {
	none := map[string]sdkauthz.Dimension{}
	fields := func(names ...string) map[string]sdkauthz.Field {
		f := map[string]sdkauthz.Field{}
		for _, n := range names {
			f[n] = sdkauthz.Field{Class: sdkauthz.Ordinary}
		}
		return f
	}
	op := func(key, typ, purpose string, audit bool) sdkauthz.Operation {
		o := sdkauthz.Operation{Key: key, ResourceType: typ, Purpose: purpose, RiskClass: "low", Audit: audit}
		if purpose != "read" {
			o.SideEffects = []string{"business_write"}
		}
		return o
	}
	return &sdkauthz.Definitions{Component: ID,
		Resources: []sdkauthz.Resource{
			{Type: MyBinding, ViewOperation: OpBindSelf, Dimensions: none, Fields: fields("user_id", "bound_at")},
			{Type: Bindings, ViewOperation: OpBindRead, Dimensions: none,
				Fields: fields("principal", "user_id", "state", "bound_at", "ended_at")},
			{Type: Callbacks, ViewOperation: OpCallbackRead, Dimensions: none,
				Fields: fields("event_id", "state", "reason", "received_at")},
			{Type: System, ViewOperation: OpRecover, Dimensions: none, Fields: fields()},
		},
		Operations: []sdkauthz.Operation{
			op(OpBindSelf, MyBinding, "manage", true),
			op(OpBindRead, Bindings, "read", false),
			op(OpBindEnd, Bindings, "manage", true),
			op(OpCallbackRead, Callbacks, "read", false),
			op(OpDeliver, System, "action", false),
			op(OpRecover, System, "read", false),
			op(OpResend, System, "read", false),
		}}
}
