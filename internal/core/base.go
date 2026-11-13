package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"

	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Config is the component's configuration as read once at the start.
type Config struct {
	APIURL, OAPIURL, LoginURL           string
	AppKey, CardTemplate                string
	AppSecret, CallbackToken, AESKey    func() string // secrets: read at each use, never logged
	LinkBase, OAuthRedirect, BindReturn string
}

// How long the purge keeps each kind of row (docs/design.md, the data): technical periods of this component, not a
// legal retention (a customer's legal retention is the deployment's, P15).
const (
	SessionKept      = 24 * time.Hour      // a binding session after its expiry
	DeliveryAfter    = 7 * 24 * time.Hour  // a delivery after its expiry ...
	DeliveryAtLeast  = 90 * 24 * time.Hour // ... and at least this long after it was made
	CallbackKept     = 180 * 24 * time.Hour
	EndedBindingKept = 400 * 24 * time.Hour
	PurgeJob         = "purge"
)

// Base is what every module works with: the runtime, the configuration and the DingTalk client.
type Base struct {
	RT     *runtime.Runtime
	Cfg    Config
	Client DingTalk
}

// NewSecret is a one-time value (a button's or a binding's state) and the digest the component keeps of it.
func NewSecret() (value, digest string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	value = base64.RawURLEncoding.EncodeToString(b)
	return value, Digest(value)
}

// Digest is a one-time value's SHA-256, hex.
func Digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
