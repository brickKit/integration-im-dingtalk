package partition

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
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

func partitionExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, testSchema+"."+name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// 迁移只建了两个月的 delivery_attempts 分区；之后的月份全靠模块的后台
// 分区维护提前建好。没建好的那个月一到，每一次写 delivery_attempts 都报
// "no partition of relation found for row"，每条派发事件都在消费时失败。
// partition.Start 是 Module.Start 里唯一的分区维护循环，所以它必须同时
// 维护周分区（outbox / inbox）与月分区（delivery_attempts）。
func TestStart_同时建好未来几周的outbox分区与未来几个月的delivery_attempts分区(t *testing.T) {
	db := testDB(t)

	month := firstOfMonth(time.Now().UTC())
	wantMonthly := []string{}
	for i := 1; i <= lookAheadMonths; i++ {
		wantMonthly = append(wantMonthly, monthPartitionName("delivery_attempts", month.AddDate(0, i, 0)))
	}
	lastWeek := weekPartitionName("event_outbox", mondayOf(time.Now().UTC()).AddDate(0, 0, 7*lookAheadWeeks))

	// 先删掉这些未来分区（里面不会有行：别的测试只按当前时间写入），否则测试库
	// 里上一次跑留下的分区会让这条测试在维护循环坏掉时照样通过。
	for _, name := range append([]string{lastWeek}, wantMonthly...) {
		if _, err := db.Exec(`DROP TABLE IF EXISTS ` + testSchema + `.` + name); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Start(ctx, db, testRole, testSchema, slog.New(slog.NewTextHandler(io.Discard, nil))) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		all := partitionExists(t, db, lastWeek)
		for _, name := range wantMonthly {
			all = all && partitionExists(t, db, name)
		}
		if all || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start 返回错误：%v", err)
	}

	if !partitionExists(t, db, lastWeek) {
		t.Errorf("期望周分区 %s 已建好", lastWeek)
	}
	for _, name := range wantMonthly {
		if !partitionExists(t, db, name) {
			t.Errorf("期望月分区 %s 已建好", name)
		}
	}
}
