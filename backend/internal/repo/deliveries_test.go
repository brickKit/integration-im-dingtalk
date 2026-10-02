package repo

import (
	"context"
	"database/sql"
	"testing"

	besdk "github.com/brickKit/be-sdk-go"
)

func mustRecordAttempt(t *testing.T, r *Repo, in RecordAttemptInput) {
	t.Helper()
	err := besdk.WithTx(context.Background(), r.db, r.role, r.schema, func(tx *sql.Tx) error {
		return RecordDeliveryAttemptTx(context.Background(), tx, in)
	})
	if err != nil {
		t.Fatalf("写投递记录失败: %v", err)
	}
}

// TestBatchGetLatestStatus_CONFIRMED优先于ACCEPTED 守"当前状态"的选取规则：
// phase 优先级 CONFIRMED > ACCEPTED，同 phase 取 attempted_at 最大的。
func TestBatchGetLatestStatus_CONFIRMED优先于ACCEPTED(t *testing.T) {
	r := testRepo(t)
	recordID := uniqueID("record")

	mustRecordAttempt(t, r, RecordAttemptInput{RecordID: recordID, Phase: PhaseAccepted, ExternalTaskID: "task-1"})
	mustRecordAttempt(t, r, RecordAttemptInput{RecordID: recordID, Phase: PhaseConfirmed, Success: true, ExternalTaskID: "task-1"})

	statuses, err := r.BatchGetLatestStatus(context.Background(), []string{recordID})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("期望 1 条最新状态，实际 %d", len(statuses))
	}
	if statuses[0].Phase != PhaseConfirmed || !statuses[0].Success {
		t.Fatalf("期望最新状态是 CONFIRMED+success，实际 phase=%q success=%v", statuses[0].Phase, statuses[0].Success)
	}
}

func TestBatchGetLatestStatus_查不到的id直接省略(t *testing.T) {
	r := testRepo(t)
	statuses, err := r.BatchGetLatestStatus(context.Background(), []string{uniqueID("never-existed")})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Fatalf("期望 0 条，实际 %d", len(statuses))
	}
}

// 契约（im.proto 的 BatchGetDeliveryStatusResponse）写明结果顺序与请求的
// record_ids 一致、查不到的省略：调用方按位置对回自己的列表。
func TestBatchGetLatestStatus_结果顺序与请求一致_查不到的省略(t *testing.T) {
	r := testRepo(t)
	first, second := uniqueID("record-z"), uniqueID("record-a") // 按字典序 second 在前
	mustRecordAttempt(t, r, RecordAttemptInput{RecordID: first, Phase: PhaseAccepted})
	mustRecordAttempt(t, r, RecordAttemptInput{RecordID: second, Phase: PhaseAccepted})

	statuses, err := r.BatchGetLatestStatus(context.Background(), []string{first, uniqueID("missing"), second})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(statuses))
	for _, st := range statuses {
		got = append(got, st.RecordID)
	}
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("期望顺序 [%s %s]，实际 %v", first, second, got)
	}
}

func TestListDeliveries_按record_id过滤(t *testing.T) {
	r := testRepo(t)
	recordA := uniqueID("record")
	recordB := uniqueID("record")
	mustRecordAttempt(t, r, RecordAttemptInput{RecordID: recordA, Phase: PhaseAccepted})
	mustRecordAttempt(t, r, RecordAttemptInput{RecordID: recordB, Phase: PhaseAccepted})

	attempts, _, err := r.ListDeliveries(context.Background(), ListInput{RecordID: recordA, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].RecordID != recordA {
		t.Fatalf("期望只查到 recordA 的 1 条，实际 %d 条", len(attempts))
	}
}
