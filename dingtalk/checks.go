package dingtalk

import "github.com/brickKit/be-sdk-go/v3/recovery"

// RecoveryChecks are the component's own checks of a full recovery (be-protocol chapter 14): none of its own; the SDK's
// cover the outbox and the external commands.
func (m *Module) RecoveryChecks(*recovery.Checks) error { return nil }
