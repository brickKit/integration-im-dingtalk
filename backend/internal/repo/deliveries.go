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

// DeliveryAttempt 是 delivery_attempts 的一行，写入即终态、不改。ACCEPTED 与
// CONFIRMED 是同一个 record_id 的两条独立记录，不是同一行原地更新。
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

// RecordDeliveryAttemptTx 只有事务版本：调用方（consumer 包）总是在同一个
// 事务里顺带把 integration.im.result.v1 写进 outbox（Outbox 模式），自带事务
// 的便捷方法会让"记了尝试却没发事件"成为可能。
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

// BatchGetLatestStatus 是按 record_id 批量查投递状态的唯一读法（调用方不该
// 逐条查，免得 N+1），供 gRPC BatchGetDeliveryStatus 用。同一个 record_id
// 可能有多条尝试（初次与每次重试各有 ACCEPTED / CONFIRMED），"当前状态"取
// CONFIRMED 优先、同 phase 取 attempted_at 最新的那条。结果按请求里
// recordIDs 的顺序排（契约约定，调用方按位置对回），查不到的省略，重复的
// id 只出现一次。
func (r *Repo) BatchGetLatestStatus(ctx context.Context, recordIDs []string) ([]*DeliveryAttempt, error) {
	if len(recordIDs) == 0 {
		return nil, nil
	}
	latest := make(map[string]*DeliveryAttempt, len(recordIDs))
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, deliverySelectColumns+`
			FROM delivery_attempts
			WHERE record_id = ANY($1::text[])
			ORDER BY record_id, (phase = 'CONFIRMED') DESC, attempted_at DESC`, recordIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d DeliveryAttempt
			if err := scanDeliveryRows(rows, &d); err != nil {
				return err
			}
			// ORDER BY 已把每个 record_id 想要的那条排在最前，只留第一次出现的。
			if _, seen := latest[d.RecordID]; !seen {
				latest[d.RecordID] = &d
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("批量查投递状态", err)
	}
	out := make([]*DeliveryAttempt, 0, len(latest))
	for _, id := range recordIDs {
		if d, ok := latest[id]; ok {
			out = append(out, d)
			delete(latest, id) // 请求里重复的 id 只返回一次
		}
	}
	return out, nil
}

// ListInput 是 ListDeliveries 的查询参数。排障视图没有数据范围
// （assembly.yaml 的 data_scopes: none），不按调用者过滤。
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
