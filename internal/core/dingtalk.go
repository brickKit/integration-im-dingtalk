package core

import "context"

// Button is one button of a card: a callback button carries the delivery, the action and its one-time value; a link
// button opens the address.
type Button struct {
	Label  string
	Action string // empty: a link button
	Value  string // the callback's value (the delivery id, the action and the one-time value)
	URL    string
}

// Card is one interactive card to one DingTalk user (ID02).
type Card struct {
	OutTrackID string // the delivery id
	UserID     string
	Title      string
	Body       string
	Buttons    []Button
}

// Send results as the external command classifies them (be-protocol P13.5).
const (
	SentOK        = "succeeded"
	NotReached    = "not_reached"
	MaybeReached  = "maybe_reached"
	RejectedClass = "rejected"
)

// DingTalk is the external system as the modules use it (internal/dingtalk implements it; the tests swap it).
type DingTalk interface {
	// SendCard sends a card: the class (SentOK, NotReached, MaybeReached, RejectedClass) and a short response that is
	// never a secret.
	SendCard(ctx context.Context, c Card) (class, response string)
	// UserOfCode exchanges an OAuth authorization code for the DingTalk user it was given to (ID10).
	UserOfCode(ctx context.Context, code string) (userID string, err error)
	// ConsentURL is the OAuth consent page for a state (ID10).
	ConsentURL(state string) string
}
