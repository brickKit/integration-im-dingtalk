package repo

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

const (
	AdapterDingtalk = "dingtalk"

	PhaseAccepted  = "ACCEPTED"
	PhaseConfirmed = "CONFIRMED"
)

// DeliveryAttempt 是 delivery_attempts 的一行——写入即终态，不改（设计
// 计划 §2）。ACCEPTED 与 CONFIRMED 是同一个 record_id 的两条独立记录，
// 不是同一行原地更新。
type DeliveryAttempt struct {
	ID             int64
	RecordID       string
	Adapter        string
	Phase          string
	Success        bool
	ErrorCode      string
	Retryable      bool
	ExternalTaskID string
	AttemptedAt    time.Time
}

type RecordAttemptInput struct {
	RecordID       string
	Phase          string
	Success        bool
	ErrorCode      string
	Retryable      bool
	ExternalTaskID string
}

// RecordDeliveryAttemptTx 供两处调用：① besdk.Consume 给的事务里，
// 提交 asyncsend_v2 后记 ACCEPTED；② 延迟回查 goroutine 自己开的
// besdk.WithTx 里，查到 getsendresult 后记 CONFIRMED（consumer 包的
// module 级说明）。两处都要求"记录 + 发 integration.im.result.v1"在同一
// 个事务里（Outbox Pattern），所以只导出 Tx 版本，不提供自带事务的
// 便捷方法——调用方（consumer 包）decide 要不要在同一个 tx 里顺带发事件。
func RecordDeliveryAttemptTx(ctx context.Context, tx *sql.Tx, in RecordAttemptInput) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO delivery_attempts
			(record_id, adapter, phase, success, error_code, retryable, external_task_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		in.RecordID, AdapterDingtalk, in.Phase, in.Success, in.ErrorCode, in.Retryable, in.ExternalTaskID)
	if err != nil {
		return fmt.Errorf("写 delivery_attempts: %w", err)
	}
	return nil
}

const deliverySelectColumns = `SELECT id, record_id, adapter, phase, success, error_code, retryable, external_task_id, attempted_at`

func scanDeliveryRows(rows *sql.Rows, d *DeliveryAttempt) error {
	return rows.Scan(&d.ID, &d.RecordID, &d.Adapter, &d.Phase, &d.Success, &d.ErrorCode, &d.Retryable, &d.ExternalTaskID, &d.AttemptedAt)
}

// BatchGetLatestStatus 是防 N+1 的唯一合法批量读方式（§3.8），供
// gRPC BatchGetDeliveryStatus 用。同一个 record_id 可能有多条尝试
// （初次 + 重试各一轮 ACCEPTED/CONFIRMED），取"当前最新状态"的判据是
// phase 优先级 CONFIRMED > ACCEPTED，同 phase 取 attempted_at 最大的
// 那条（同迁移文件顶部注释）。
func (r *Repo) BatchGetLatestStatus(ctx context.Context, recordIDs []string) ([]*DeliveryAttempt, error) {
	if len(recordIDs) == 0 {
		return nil, nil
	}
	var out []*DeliveryAttempt
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, deliverySelectColumns+`
			FROM delivery_attempts
			WHERE record_id = ANY($1::text[])
			ORDER BY record_id, (phase = 'CONFIRMED') DESC, attempted_at DESC`, recordIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := make(map[string]bool, len(recordIDs))
		for rows.Next() {
			var d DeliveryAttempt
			if err := scanDeliveryRows(rows, &d); err != nil {
				return err
			}
			// ORDER BY 已经把每个 record_id 最想要的那条排在最前面——
			// 用 seen 只取每个 record_id 第一次出现的那行（同 DISTINCT ON
			// 的效果，这里不用 DISTINCT ON 是因为它要求 ORDER BY 的前缀
			// 列必须与 DISTINCT ON 的列完全一致，写法上不如显式判断直观）。
			if seen[d.RecordID] {
				continue
			}
			seen[d.RecordID] = true
			out = append(out, &d)
		}
		return rows.Err()
	})
	return out, wrap("批量查投递状态", err)
}

// ListInput 是 ListDeliveries 的查询参数——排障视图，data_scopes: none，
// 不做任何数据权限过滤（assembly.yaml 显式声明）。
type ListInput struct {
	RecordID string // 空 = 不筛
	Phase    string // 空 = 不筛
	Cursor   string
	PageSize int32
}

func (r *Repo) ListDeliveries(ctx context.Context, in ListInput) ([]*DeliveryAttempt, string, error) {
	pageSize := in.PageSize
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	var afterID int64
	if in.Cursor != "" {
		id, err := strconv.ParseInt(in.Cursor, 10, 64)
		if err != nil {
			return nil, "", fmt.Errorf("%w: cursor 不合法：%q", ErrInvalidArgument, in.Cursor)
		}
		afterID = id
	}

	query := deliverySelectColumns + ` FROM delivery_attempts WHERE id > $1`
	args := []any{afterID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if in.RecordID != "" {
		query += ` AND record_id = ` + arg(in.RecordID)
	}
	if in.Phase != "" {
		query += ` AND phase = ` + arg(in.Phase)
	}
	query += ` ORDER BY id LIMIT ` + arg(int64(pageSize)+1)

	var out []*DeliveryAttempt
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d DeliveryAttempt
			if err := scanDeliveryRows(rows, &d); err != nil {
				return err
			}
			out = append(out, &d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", wrap("列投递记录", err)
	}

	nextCursor := ""
	if int32(len(out)) > pageSize {
		out = out[:pageSize]
		nextCursor = strconv.FormatInt(out[len(out)-1].ID, 10)
	}
	return out, nextCursor, nil
}
