// Package partition 是 Module.Start 的后台循环之一：为 event_outbox/
// event_inbox 自动创建未来的周分区（决策 54、§11.5.1），同
// infra-notification/erp-inventory 的既有实现。
//
// delivery_attempts 的月分区维护在 monthly.go——它和这里的周分区是两套
// 独立的窗口逻辑（设计计划 §7：投递尝试按月，且归档窗口全系统最短，
// 只有 1 个月）。
//
// dingtalk_user_map/dingtalk_token 不在这里——它们不分区（设计计划
// §2、§7）。
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

func Start(ctx context.Context, db *sql.DB, role, schema string, logger *slog.Logger) error {
	if err := ensureAllWeekly(ctx, db, role, schema); err != nil {
		logger.Error("周分区维护失败", "error", err)
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := ensureAllWeekly(ctx, db, role, schema); err != nil {
				logger.Error("周分区维护失败", "error", err)
			}
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

// ensurePartition 用 to_regclass 先确认分区存不存在，不存在才建（同
// erp-inventory partition.go 的既有判据：不能反过来"先建、报 already
// exists 就忽略"）。name 由调用方算好传入，周/月两套格式不同（见
// monthly.go），本函数本身与"周还是月"无关。
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
