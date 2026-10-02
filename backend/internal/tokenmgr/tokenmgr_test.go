package tokenmgr

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/dingtalk"
	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/repo"
	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/testlock"
)

var zeroTime time.Time

func futureTime() time.Time { return time.Now().Add(2 * time.Hour) }

func testRepo(t *testing.T) *repo.Repo {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_PG_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	testlock.TokenRow(t, db)
	return repo.New(db, "integration_im_dingtalk_rw", "integration_im_dingtalk")
}

// fakeTokenServer 统计 /gettoken 被真的调了几次——用来验证"缓存没过期就
// 不该真的再调钉钉"这条断言。
func fakeTokenServer(t *testing.T, token string) (*httptest.Server, *int64) {
	t.Helper()
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "access_token": token, "expires_in": 7200})
	}))
	return srv, &calls
}

func TestEnsureValid_没缓存时真的调一次钉钉(t *testing.T) {
	r := testRepo(t)
	srv, calls := fakeTokenServer(t, "tok-fresh")
	defer srv.Close()

	// 先清空缓存，保证这个测试不受其它测试残留状态影响。
	if err := r.SetToken(context.Background(), "", zeroTime); err != nil {
		t.Fatal(err)
	}

	m := New(r, dingtalk.NewClient(srv.URL, "ak", "as"))
	token, err := m.EnsureValid(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "tok-fresh" {
		t.Fatalf("期望 tok-fresh，实际 %q", token)
	}
	if atomic.LoadInt64(calls) != 1 {
		t.Fatalf("期望恰好调 1 次 /gettoken，实际 %d 次", atomic.LoadInt64(calls))
	}
}

func TestEnsureValid_缓存未过期时不重复调钉钉(t *testing.T) {
	r := testRepo(t)
	srv, calls := fakeTokenServer(t, "should-not-be-used")
	defer srv.Close()

	if err := r.SetToken(context.Background(), "tok-cached", futureTime()); err != nil {
		t.Fatal(err)
	}

	m := New(r, dingtalk.NewClient(srv.URL, "ak", "as"))
	token, err := m.EnsureValid(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "tok-cached" {
		t.Fatalf("期望直接用缓存 tok-cached，实际 %q", token)
	}
	if atomic.LoadInt64(calls) != 0 {
		t.Fatalf("缓存没过期，期望 0 次 /gettoken 调用，实际 %d 次", atomic.LoadInt64(calls))
	}
}

func TestRefresh_无条件调钉钉(t *testing.T) {
	r := testRepo(t)
	srv, calls := fakeTokenServer(t, "tok-forced")
	defer srv.Close()

	if err := r.SetToken(context.Background(), "tok-old", futureTime()); err != nil {
		t.Fatal(err)
	}

	m := New(r, dingtalk.NewClient(srv.URL, "ak", "as"))
	token, err := m.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "tok-forced" {
		t.Fatalf("期望强制刷新拿到 tok-forced，实际 %q", token)
	}
	if atomic.LoadInt64(calls) != 1 {
		t.Fatalf("Refresh 期望恰好调 1 次 /gettoken，实际 %d 次", atomic.LoadInt64(calls))
	}
}

// 并发消息同时发现 token 失效时，只该有一个 goroutine 真的去调 gettoken，
// 其余等它的结果（钉钉的 gettoken 有频控）。fake 服务器故意慢一点，把
// "都判断失效、都去刷新"的窗口拉开。
func TestEnsureValid_并发调用只申请一次token(t *testing.T) {
	r := testRepo(t)
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt64(&calls, 1)
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "access_token": "tok-once", "expires_in": 7200})
	}))
	defer srv.Close()

	if err := r.SetToken(context.Background(), "", zeroTime); err != nil {
		t.Fatal(err)
	}

	m := New(r, dingtalk.NewClient(srv.URL, "ak", "as"))
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	tokens := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := m.EnsureValid(context.Background())
			errs <- err
			tokens <- tok
		}()
	}
	wg.Wait()
	close(errs)
	close(tokens)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for tok := range tokens {
		if tok != "tok-once" {
			t.Fatalf("期望每个调用方都拿到 tok-once，实际 %q", tok)
		}
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("%d 个并发 EnsureValid 期望只调 1 次 /gettoken，实际 %d 次", n, got)
	}
}
