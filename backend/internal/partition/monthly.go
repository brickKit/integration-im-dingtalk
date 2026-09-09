// delivery_attempts 的月分区维护——本组件唯一按月分区的表（设计计划
// §7：归档窗口全系统最短，只热 1 个月，但仍然要提前建好未来分区，否则
// 跨月写入直接崩）。
package partition

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

const lookAheadMonths = 3 // 提前建好当前月 + 未来 3 个月（同 infra-notification 的既有余量）

var monthlyPartitionedTables = []string{"delivery_attempts"}

func StartMonthly(ctx context.Context, db *sql.DB, role, schema string, logger *slog.Logger) error {
	if err := ensureAllMonthly(ctx, db, role, schema); err != nil {
		logger.Error("月分区维护失败", "error", err)
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := ensureAllMonthly(ctx, db, role, schema); err != nil {
				logger.Error("月分区维护失败", "error", err)
			}
		}
	}
}

func ensureAllMonthly(ctx context.Context, db *sql.DB, role, schema string) error {
	return besdk.WithTx(ctx, db, role, schema, func(tx *sql.Tx) error {
		monthStart := firstOfMonth(time.Now().UTC())
		for i := 0; i <= lookAheadMonths; i++ {
			from := monthStart.AddDate(0, i, 0)
			to := from.AddDate(0, 1, 0)
			for _, table := range monthlyPartitionedTables {
				if err := ensurePartition(ctx, tx, table, monthPartitionName(table, from), from, to); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func monthPartitionName(table string, from time.Time) string {
	return fmt.Sprintf("%s_%04d_%02d_01", table, from.Year(), from.Month())
}

func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// ⚠️ 不需要额外 ALTER TABLE ... OWNER TO——真机验证过（infra-notification
// 那次核实的同一个结论）：SET LOCAL ROLE 之后 CREATE TABLE，新分区的
// owner 就是当前角色本身；迁移建的初始分区所有者虽然不是
// integration_im_dingtalk_rw，但这个 schema 有 ALTER DEFAULT PRIVILEGES
// 授予的 DML 权限，不看 owner。
