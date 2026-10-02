// Package partition 是 Module.Start 的后台循环之一，提前建好分区表未来的
// 分区：event_outbox / event_inbox 按周，delivery_attempts 按月
// （monthly.go）。没提前建好的那个周期一到，写这些表的每一条 INSERT 都报
// "no partition of relation found for row"。
//
// dingtalk_user_map、dingtalk_token 不分区，不在这里。
package partition

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

const (
	checkInterval  = 24 * time.Hour
	lookAheadWeeks = 4
)

var weeklyPartitionedTables = []string{"event_outbox", "event_inbox"}

// Start 启动时维护一次，之后每 checkInterval 一次，直到 ctx 取消。周分区与
// 月分区各自失败、各自记日志，一边失败不耽误另一边。
func Start(ctx context.Context, db *sql.DB, role, schema string, logger *slog.Logger) error {
	maintain := func() {
		if err := ensureAllWeekly(ctx, db, role, schema); err != nil {
			logger.Error("周分区维护失败", "error", err)
		}
		if err := ensureAllMonthly(ctx, db, role, schema); err != nil {
			logger.Error("月分区维护失败", "error", err)
		}
	}
	maintain()

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			maintain()
		}
	}
}

func ensureAllWeekly(ctx context.Context, db *sql.DB, role, schema string) error {
	return besdk.WithTx(ctx, db, role, schema, func(tx *sql.Tx) error {
		weekStart := mondayOf(time.Now().UTC())
		for i := 0; i <= lookAheadWeeks; i++ {
			from := weekStart.AddDate(0, 0, 7*i)
			to := from.AddDate(0, 0, 7)
			for _, table := range weeklyPartitionedTables {
				if err := ensurePartition(ctx, tx, table, weekPartitionName(table, from), from, to); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func weekPartitionName(table string, from time.Time) string {
	return fmt.Sprintf("%s_%s", table, from.Format("2006_01_02"))
}

func mondayOf(t time.Time) time.Time {
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return d.AddDate(0, 0, -(weekday - 1))
}

// ensurePartition 先用 to_regclass 确认分区在不在，不在才建：不能反过来
// "先建、报 already exists 再忽略"——报错会让整个事务进入 aborted 状态，
// 同一事务里后面的分区全建不了。name 由调用方按周或月的格式算好传入。
func ensurePartition(ctx context.Context, tx *sql.Tx, table, name string, from, to time.Time) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		return fmt.Errorf("检查分区是否存在 %s: %w", name, err)
	}
	if exists {
		return nil
	}
	stmt := fmt.Sprintf(
		`CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
		name, table, from.Format("2006-01-02"), to.Format("2006-01-02"),
	)
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("建分区 %s: %w", name, err)
	}
	return nil
}
