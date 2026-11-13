// Command seed fills a running integration/im-dingtalk with the seed data for people (be-assembly-standard's seed and
// test data conventions). The bindings are the users' own (each binds through DingTalk's OAuth; a seed cannot), so it
// grants, by the authorization admin: binding one's own DingTalk user to the seed user, the purchasing and finance
// leads, the buyers and the cashier; listing and ending bindings and reading the quarantined callbacks to the seed
// user (the maintainer).
//
// A second run changes nothing. It waits until the grants reach the component.
//
//	SEED_IAM_URL (http://localhost:18200)    the identity member's development exchange
//	SEED_AUTHZ_URL (http://localhost:18223)  the authorization member's effect point
//	SEED_ADMIN (test.alice)                  the authorization admin (the install claim)
//	SEED_USER (test.bob), SEED_PASSWORD      the seed user; the test accounts' password
//	INTEGRATION_IM_DINGTALK_URL (http://localhost:18207)
package main

import (
	"fmt"
	"os"
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/seed"
)

const prefix = "「本地测试」"

// users are the accounts that bind their own DingTalk user: the approvers and the ones who submit and follow.
var users = []string{"test.buyer.lead", "test.finance.lead", "test.buyer.cn", "test.cashier.cn"}

func main() {
	base := env("INTEGRATION_IM_DINGTALK_URL", "http://localhost:18207")
	if !seed.Ready(base) {
		fail("integration/im-dingtalk at %s is not ready", base)
	}
	pw := os.Getenv("SEED_PASSWORD")
	if pw == "" {
		fail("SEED_PASSWORD is not set (the test accounts' password in Casdoor)")
	}
	iam, user, admin := env("SEED_IAM_URL", "http://localhost:18200"), env("SEED_USER", "test.bob"), env("SEED_ADMIN", "test.alice")
	accounts, err := seed.Accounts(iam, pw, append([]string{admin, user}, users...)...)
	check(err)
	a, err := seed.NewAuthz(env("SEED_AUTHZ_URL", "http://localhost:18223"), accounts[admin].Token, "integration/im-dingtalk")
	check(err)
	self := []sdkauthz.Segment{segment("integration.im_dingtalk.my_binding", "integration.im_dingtalk.my_binding.manage")}
	maintain := append(append([]sdkauthz.Segment{}, self...),
		segment("integration.im_dingtalk.binding", "integration.im_dingtalk.binding.read"),
		segment("integration.im_dingtalk.binding", "integration.im_dingtalk.binding.end"),
		segment("integration.im_dingtalk.callback", "integration.im_dingtalk.callback.read"))
	_, err = a.GrantRoles([]seed.RoleSpec{
		{Key: "my-dingtalk", Name: prefix + "绑定我的钉钉", To: users, Segments: self},
		{Key: "maintain", Name: prefix + "钉钉渠道维护", To: []string{user}, Segments: maintain},
	}, accounts)
	check(err)
	for _, u := range append([]string{user}, users...) {
		// a read: answered 200 once the grant has reached the component (WaitGranted is for writes, a 400 on the body)
		code, out, err := seed.NewClient(base, accounts[u].Token).UntilGranted(time.Minute, "GET", "/api/im-dingtalk/bindings/me", nil)
		check(err)
		if code != 200 {
			fail("%s: GET /api/im-dingtalk/bindings/me answers %d %v", u, code, out)
		}
	}
	fmt.Printf("seed integration/im-dingtalk: binding granted to %d accounts, maintenance to %s\n", len(users)+1, user)
}

// segment grants an operation on a resource of the component (no dimension).
func segment(resource, op string) sdkauthz.Segment {
	s := seed.Segment(resource, op)
	s.Atoms = map[string]sdkauthz.Atom{}
	return s
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed integration/im-dingtalk: "+format+"\n", a...)
	os.Exit(1)
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}
