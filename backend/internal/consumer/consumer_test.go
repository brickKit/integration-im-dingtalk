package consumer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go"

	"github.com/brickKit/integration-im-dingtalk/backend/internal/dingtalk"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/repo"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/tokenmgr"
)

const (
	testRole   = "integration_im_dingtalk_rw"
	testSchema = "integration_im_dingtalk"
)

func testDB(t *testing.T) *sql.DB {
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
	return db
}

func natsURLForTest(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("TEST_NATS_URL"); u != "" {
		return u
	}
	return nats.DefaultURL
}

func publishEvent(t *testing.T, nc *nats.Conn, subject, aggregateID string, version int64, payload string) {
	t.Helper()
	msg := &nats.Msg{Subject: subject, Data: []byte(payload), Header: nats.Header{}}
	msg.Header.Set("X-Aggregate-Id", aggregateID)
	msg.Header.Set("X-Version", strconv.FormatInt(version, 10))
	msg.Header.Set("X-Hop-Count", "0")
	if err := nc.PublishMsg(msg); err != nil {
		t.Fatal(err)
	}
}

func outboxCount(t *testing.T, db *sql.DB, subject, aggregateID string) int {
	t.Helper()
	var n int
	err := besdk.WithTx(context.Background(), db, testRole, testSchema, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM event_outbox WHERE subject = $1 AND aggregate_id = $2`,
			subject, aggregateID).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// fakeDingtalk 模拟钉钉四个接口（httptest，见 dingtalk 包顶部注释——本
// 阶段没有真实沙盒，这里验证的是"消费到派发事件之后内部编排对不对"，
// 不是"钉钉真的这样回"）。sendResultDone 控制 getsendresult 第几次调用
// 才返回"已完成"，用来测延迟回查的退避循环。
type fakeDingtalk struct {
	sendResultCallsBeforeDone int32
	sendResultCalls           int32
	failUserIDs               []string
}

func newFakeDingtalkServer(f *fakeDingtalk) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/gettoken", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "access_token": "tok", "expires_in": 7200})
	})
	mux.HandleFunc("/topapi/v2/user/getbymobile", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "result": map[string]string{"userid": "u-fake"}})
	})
	mux.HandleFunc("/topapi/message/corpconversation/asyncsend_v2", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "task_id": 12345})
	})
	mux.HandleFunc("/topapi/message/corpconversation/getsendresult", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&f.sendResultCalls, 1)
		if n <= f.sendResultCallsBeforeDone {
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok",
				"send_result": map[string]any{"status": 1, "progress_in_percent": 50}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok",
			"send_result": map[string]any{"status": 2, "progress_in_percent": 100, "failed_user_id_list": f.failUserIDs}})
	})
	return httptest.NewServer(mux)
}

func dispatchPayloadJSON(recordID string, attempt int, targetAdapters []string, phone string) string {
	b, _ := json.Marshal(map[string]any{
		"record_id": recordID, "attempt": attempt, "target_adapters": targetAdapters,
		"recipient_phone": phone, "title": "t", "body": "测试通知内容",
	})
	return string(b)
}

func runConsumeFor(t *testing.T, db *sql.DB, nc *nats.Conn, handle func(context.Context, *sql.Tx, besdk.Event) error, waitFor time.Duration, publish func(*nats.Conn)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitFor+2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = besdk.Consume(ctx, nc, db, testRole, testSchema, "infra.notification.dispatch.im.v1", handle)
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	publish(nc)
	nc.Flush()
	time.Sleep(waitFor)
	cancel()
	<-done
}

// TestDispatchHandler_target_adapters不含dingtalk时直接丢弃 是设计计划
// §4 明文要求的自我过滤断言：channel:im 族的适配器共用同一个 subject，
// NATS 核心是广播，漏了这一步会导致装了多个 IM 通道的客户每条通知
// 收到多遍。
func TestDispatchHandler_target_adapters不含dingtalk时直接丢弃(t *testing.T) {
	db := testDB(t)
	nc, err := nats.Connect(natsURLForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	f := &fakeDingtalk{sendResultCallsBeforeDone: 0}
	srv := newFakeDingtalkServer(f)
	defer srv.Close()

	r := repo.New(db, testRole, testSchema)
	client := dingtalk.NewClient(srv.URL, "ak", "as")
	tm := tokenmgr.New(r, client)
	handler := dispatchHandler(db, testRole, testSchema, r, client, tm, 1, 200*time.Millisecond, slog.Default())

	recordID := fmt.Sprintf("record-%d", time.Now().UnixNano())
	runConsumeFor(t, db, nc, handler, 300*time.Millisecond, func(nc *nats.Conn) {
		publishEvent(t, nc, "infra.notification.dispatch.im.v1", recordID, 1,
			dispatchPayloadJSON(recordID, 1, []string{"wecom"}, "13800000000"))
	})

	if outboxCount(t, db, "integration.im.result.v1", recordID) != 0 {
		t.Fatal("target_adapters 不含 dingtalk，期望不发任何结果事件")
	}
	statuses, err := r.BatchGetLatestStatus(context.Background(), []string{recordID})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Fatal("target_adapters 不含 dingtalk，期望不落任何 delivery_attempts")
	}
}

// TestDispatchHandler_成功提交后ACCEPTED_延迟回查后CONFIRMED 是本组件
// 最核心的端到端链路：真订阅、真发布、真等待，走完 asyncsend_v2 →
// ACCEPTED → 延迟回查 getsendresult（第一轮未完成，第二轮完成）→
// CONFIRMED 成功，两个阶段各发一条 integration.im.result.v1，version
// 严格递增（attempt*10+phase_ordinal）。
func TestDispatchHandler_成功提交后ACCEPTED_延迟回查后CONFIRMED(t *testing.T) {
	db := testDB(t)
	nc, err := nats.Connect(natsURLForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	f := &fakeDingtalk{sendResultCallsBeforeDone: 1} // 第一轮"未完成"，第二轮"完成"
	srv := newFakeDingtalkServer(f)
	defer srv.Close()

	r := repo.New(db, testRole, testSchema)
	client := dingtalk.NewClient(srv.URL, "ak", "as")
	tm := tokenmgr.New(r, client)
	confirmDelay := 200 * time.Millisecond
	handler := dispatchHandler(db, testRole, testSchema, r, client, tm, 1, confirmDelay, slog.Default())

	recordID := fmt.Sprintf("record-%d", time.Now().UnixNano())
	// 延迟回查在独立 goroutine 里跑，退避两轮（200ms + 400ms），等待时间
	// 要盖过这段时间。
	runConsumeFor(t, db, nc, handler, 1200*time.Millisecond, func(nc *nats.Conn) {
		publishEvent(t, nc, "infra.notification.dispatch.im.v1", recordID, 1,
			dispatchPayloadJSON(recordID, 1, []string{"dingtalk"}, "13800000000"))
	})

	statuses, err := r.BatchGetLatestStatus(context.Background(), []string{recordID})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("期望 1 条最新状态，实际 %d", len(statuses))
	}
	if statuses[0].Phase != repo.PhaseConfirmed || !statuses[0].Success {
		t.Fatalf("期望最新状态 CONFIRMED+success，实际 phase=%q success=%v", statuses[0].Phase, statuses[0].Success)
	}
	if statuses[0].ExternalTaskID != "12345" {
		t.Fatalf("期望 external_task_id=12345，实际 %q", statuses[0].ExternalTaskID)
	}

	deliveries, _, err := r.ListDeliveries(context.Background(), repo.ListInput{RecordID: recordID, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("期望 ACCEPTED+CONFIRMED 各一条，共 2 条，实际 %d 条", len(deliveries))
	}

	if outboxCount(t, db, "integration.im.result.v1", recordID) != 2 {
		t.Fatal("期望发 2 条 integration.im.result.v1（ACCEPTED + CONFIRMED）")
	}
	versions := resultEventVersions(t, db, recordID)
	if len(versions) != 2 || versions[0] != 11 || versions[1] != 12 {
		t.Fatalf("期望 version 序列 [11, 12]（attempt=1 的 ACCEPTED=11、CONFIRMED=12），实际 %v", versions)
	}
}

// TestDispatchHandler_收件人不在企业内_不可重试且清缓存 验证设计计划
// §4.1 的分工：asyncsend_v2 本身失败（这里用一个总是失败的 fake server
// 模拟"用户不存在"）时直接落 CONFIRMED 失败，不经过 ACCEPTED。
func TestDispatchHandler_收件人不在企业内_不可重试且清缓存(t *testing.T) {
	db := testDB(t)
	nc, err := nats.Connect(natsURLForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	// ⚠️ 缓存预先写好一条 stale-userid（模拟"这个人之前解析过，后来离职
	// 了，缓存还没失效"）——resolveUserID 命中缓存后不会再调
	// getbymobile，真正触发失败的是发送本身（asyncsend_v2 返回"这个人
	// 不在企业内"，这才是设计计划 §2 过期策略描述的真实场景：发送失败
	// 且错误码是"用户不存在"时才清缓存，不是解析阶段）。
	mux := http.NewServeMux()
	mux.HandleFunc("/gettoken", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "access_token": "tok", "expires_in": 7200})
	})
	mux.HandleFunc("/topapi/message/corpconversation/asyncsend_v2", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 60121, "errmsg": "该手机号不在企业内"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := repo.New(db, testRole, testSchema)
	client := dingtalk.NewClient(srv.URL, "ak", "as")
	tm := tokenmgr.New(r, client)
	handler := dispatchHandler(db, testRole, testSchema, r, client, tm, 1, 200*time.Millisecond, slog.Default())

	phone := fmt.Sprintf("139%08d", time.Now().UnixNano()%100000000)
	if err := r.SetUserID(context.Background(), phone, "stale-userid"); err != nil {
		t.Fatal(err)
	}

	recordID := fmt.Sprintf("record-%d", time.Now().UnixNano())
	runConsumeFor(t, db, nc, handler, 300*time.Millisecond, func(nc *nats.Conn) {
		publishEvent(t, nc, "infra.notification.dispatch.im.v1", recordID, 1,
			dispatchPayloadJSON(recordID, 1, []string{"dingtalk"}, phone))
	})

	statuses, err := r.BatchGetLatestStatus(context.Background(), []string{recordID})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Phase != repo.PhaseConfirmed || statuses[0].Success || statuses[0].Retryable {
		t.Fatalf("期望 1 条 CONFIRMED+失败+不可重试，实际 %+v", statuses)
	}

	if _, ok, err := r.GetUserID(context.Background(), phone); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("用户不在企业内失败后，期望缓存被清掉")
	}
}

func resultEventVersions(t *testing.T, db *sql.DB, aggregateID string) []int64 {
	t.Helper()
	var versions []int64
	err := besdk.WithTx(context.Background(), db, testRole, testSchema, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT version FROM event_outbox WHERE subject = 'integration.im.result.v1' AND aggregate_id = $1 ORDER BY id`, aggregateID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v int64
			if err := rows.Scan(&v); err != nil {
				return err
			}
			versions = append(versions, v)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return versions
}
