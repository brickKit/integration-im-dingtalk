// delivery_attempts 的月分区。投递尝试是纯排障数据，热数据只留 1 个月，
// 但未来的分区照样要提前建好，否则跨月第一条写入就失败。
package partition

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

const lookAheadMonths = 3 // 当前月 + 未来 3 个月

var monthlyPartitionedTables = []string{"delivery_attempts"}

// 新分区不用另外 ALTER TABLE … OWNER TO：维护循环在 SET LOCAL ROLE 之后
// CREATE TABLE，新分区的属主就是本组件的角色。
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
