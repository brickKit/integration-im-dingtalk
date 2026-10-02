// Package consumer 是本组件发消息的唯一入口：没有"发消息"的 rpc，发消息
// 只能通过事件进来。消费 channel:im 族的派发事件
// infra.notification.dispatch.im.v1，先判断 target_adapters 里有没有
// "dingtalk"（族内全部适配器订阅同一个 subject，NATS 核心订阅是广播，漏了
// 这一步，装了几个 IM 通道的客户每条通知就收到几遍），有则解析 userid →
// 调钉钉 asyncsend_v2 → 记 ACCEPTED → 延迟回查 getsendresult → 记
// CONFIRMED，两个阶段各发一条 integration.im.result.v1。
package consumer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/nats-io/nats.go"

	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/dingtalk"
	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/repo"
	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/tokenmgr"
)

// dispatchSubject 是 channel:im 族的派发事件，resultSubject 是族的结果事件。
// 两条都是族级 subject，不带 dingtalk：加一个 IM 适配器不需要改通知中心。
const (
	dispatchSubject = "infra.notification.dispatch.im.v1"
	resultSubject   = "integration.im.result.v1"
)

// confirmMaxPolls 是延迟回查 getsendresult 的轮数上限。每轮没出结果就把
// 等待时长翻倍；轮数用完仍没结果，记一条可重试的超时失败，不让记录无限期
// 停在 ACCEPTED。
const confirmMaxPolls = 4

func Start(ctx context.Context, db *sql.DB, role, schema string, nc *nats.Conn,
	r *repo.Repo, client *dingtalk.Client, tm *tokenmgr.Manager, agentID int64,
	confirmInitialDelay time.Duration, logger *slog.Logger) error {

	handler := dispatchHandler(db, role, schema, r, client, tm, agentID, confirmInitialDelay, logger)
	return besdk.Consume(ctx, nc, db, role, schema, dispatchSubject, handler)
}

// deliverer 持有投递一条派发事件要用到的全部依赖。
type deliverer struct {
	db           *sql.DB
	role, schema string
	repo         *repo.Repo
	client       *dingtalk.Client
	tm           *tokenmgr.Manager
	agentID      int64
	confirmDelay time.Duration // 第一次回查前等多久；生产 5 秒，测试注入更短的值
	logger       *slog.Logger
}

func dispatchHandler(db *sql.DB, role, schema string, r *repo.Repo, client *dingtalk.Client,
	tm *tokenmgr.Manager, agentID int64, confirmInitialDelay time.Duration, logger *slog.Logger) func(context.Context, *sql.Tx, besdk.Event) error {
	d := &deliverer{db: db, role: role, schema: schema, repo: r, client: client, tm: tm,
		agentID: agentID, confirmDelay: confirmInitialDelay, logger: logger}
	return d.handle
}

type dispatchPayload struct {
	RecordID       string   `json:"record_id"`
	Attempt        int      `json:"attempt"`
	TargetAdapters []string `json:"target_adapters"`
	RecipientPhone string   `json:"recipient_phone"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
}

func containsAdapter(adapters []string, name string) bool {
	for _, a := range adapters {
		if a == name {
			return true
		}
	}
	return false
}

// handle 在 besdk.Consume 给的事务里处理一条派发事件：inbox 去重、version
// 单调由 SDK 做完，这里只编排投递。
func (d *deliverer) handle(ctx context.Context, tx *sql.Tx, ev besdk.Event) error {
	var p dispatchPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("解析 %s payload: %w", ev.Subject, err)
	}
	if !containsAdapter(p.TargetAdapters, repo.AdapterDingtalk) {
		return nil // 不是发给钉钉的：族内别的适配器会处理它
	}
	if p.Attempt <= 0 {
		p.Attempt = 1 // 宽容反序列化：payload 缺这个字段时当作第一次投递
	}

	userid, err := d.resolveUserID(ctx, p.RecipientPhone)
	if err != nil {
		return d.recordFailureTx(ctx, tx, p, err)
	}
	token, err := d.tm.EnsureValid(ctx)
	if err != nil {
		return d.recordFailureTx(ctx, tx, p, fmt.Errorf("获取 access_token 失败: %w", err))
	}
	taskID, err := d.send(ctx, token, userid, p.Body)
	if err != nil {
		return d.recordFailureTx(ctx, tx, p, err)
	}

	if err := recordResultTx(ctx, tx, d.schema, result{
		RecordID: p.RecordID, Phase: repo.PhaseAccepted, Attempt: p.Attempt,
		ExternalTaskID: strconv.FormatInt(taskID, 10),
	}); err != nil {
		return err
	}

	// 延迟回查在独立 goroutine 里做，不在这个事务里 sleep：那会一直占着一个
	// 数据库连接和这条消息的 inbox 声明，拖住整个消费循环。ctx 是
	// Module.Start 的长生命周期 ctx，组件关闭时取消，goroutine 跟着退出。
	go d.confirmLater(ctx, p, userid, taskID)
	return nil
}

// resolveUserID 先查手机号缓存，没有才调钉钉 getbymobile（那个接口有频控，
// 不能每发一条查一次）。
func (d *deliverer) resolveUserID(ctx context.Context, phone string) (string, error) {
	if cached, ok, err := d.repo.GetUserID(ctx, phone); err != nil {
		return "", fmt.Errorf("查手机号缓存: %w", err)
	} else if ok {
		return cached, nil
	}

	token, err := d.tm.EnsureValid(ctx)
	if err != nil {
		return "", fmt.Errorf("获取 access_token 失败: %w", err)
	}
	userid, err := d.client.GetUserIDByMobile(ctx, token, phone)
	if err != nil && dingtalk.IsTokenExpired(err) {
		// access_token 过期是本组件自己的事：刷新一次再试，不消耗通知中心的重试次数。
		token, rerr := d.tm.Refresh(ctx)
		if rerr != nil {
			return "", fmt.Errorf("刷新 access_token 失败: %w", rerr)
		}
		userid, err = d.client.GetUserIDByMobile(ctx, token, phone)
	}
	if err != nil {
		return "", err
	}
	if err := d.repo.SetUserID(ctx, phone, userid); err != nil {
		return "", fmt.Errorf("写手机号缓存: %w", err)
	}
	return userid, nil
}

// send 调 asyncsend_v2。只有"access_token 过期"这一种错误自己刷新后重试
// 一次，其余错误原样返回给调用方分类。
func (d *deliverer) send(ctx context.Context, token, userid, content string) (int64, error) {
	taskID, err := d.client.SendWorkNotification(ctx, token, d.agentID, userid, content)
	if err != nil && dingtalk.IsTokenExpired(err) {
		newToken, rerr := d.tm.Refresh(ctx)
		if rerr != nil {
			return 0, fmt.Errorf("刷新 access_token 失败: %w", rerr)
		}
		return d.client.SendWorkNotification(ctx, newToken, d.agentID, userid, content)
	}
	return taskID, err
}

// recordFailureTx 处理钉钉从未受理的情况（解析 userid 失败、拿不到 token、
// asyncsend_v2 本身失败）：没有 ACCEPTED 阶段可言，直接记一条 CONFIRMED
// 失败。错误分类决定 retryable，"用户不存在"类还要清掉手机号缓存。
func (d *deliverer) recordFailureTx(ctx context.Context, tx *sql.Tx, p dispatchPayload, sendErr error) error {
	retryable, errorCode, evictCache := dingtalk.ClassifyError(sendErr)
	if evictCache {
		// 缓存清理用 DeleteUserID 自带的独立事务：手机号缓存只是性能缓存，
		// 不必与 delivery_attempts 原子；这个事务后面回滚了，缓存已清也无妨，
		// 下次重新解析一次而已。
		if err := d.repo.DeleteUserID(ctx, p.RecipientPhone); err != nil {
			return fmt.Errorf("清理失效手机号缓存: %w", err)
		}
	}
	return recordResultTx(ctx, tx, d.schema, result{
		RecordID: p.RecordID, Phase: repo.PhaseConfirmed, Attempt: p.Attempt,
		ErrorCode: errorCode, Retryable: retryable,
	})
}

// confirmLater 隔一段时间查 getsendresult，没出结果就退避再查，查到结果或
// 轮数用完后记 CONFIRMED 并发结果事件。它不持有派发事件那条事务（早已
// 提交），自己开事务写。
func (d *deliverer) confirmLater(ctx context.Context, p dispatchPayload, userid string, taskID int64) {
	backoff := d.confirmDelay
	for i := 0; i < confirmMaxPolls; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2

		token, err := d.tm.EnsureValid(ctx)
		if err != nil {
			d.logger.Warn("延迟回查获取 access_token 失败，稍后重试", "record_id", p.RecordID, "error", err)
			continue
		}
		sr, err := d.client.GetSendResult(ctx, token, d.agentID, taskID, userid)
		if err != nil {
			if dingtalk.IsTokenExpired(err) {
				if _, rerr := d.tm.Refresh(ctx); rerr != nil {
					d.logger.Warn("延迟回查刷新 access_token 失败", "record_id", p.RecordID, "error", rerr)
				}
			} else {
				d.logger.Warn("延迟回查 getsendresult 失败，稍后重试", "record_id", p.RecordID, "error", err)
			}
			continue
		}
		if !sr.Done {
			continue
		}

		retryable, errorCode := classifySendResult(sr)
		if err := d.recordConfirmed(ctx, p, sr.Success, errorCode, retryable, taskID); err != nil {
			d.logger.Error("落库/发布 CONFIRMED 结果失败", "record_id", p.RecordID, "error", err)
		}
		return
	}

	// 轮数用完仍没结果：当作可重试的超时，主动发一条 CONFIRMED 失败，比让
	// 记录无限期停在 ACCEPTED 好（通知中心据此决定要不要重发）。
	if err := d.recordConfirmed(ctx, p, false, "GET_SEND_RESULT_TIMEOUT", true, taskID); err != nil {
		d.logger.Error("落库/发布超时结果失败", "record_id", p.RecordID, "error", err)
	}
}

// classifySendResult 把 getsendresult 的名单归类翻成"值不值得重试"：
// 无效 / 禁止名单（这个人本来就收不到）不值得，失败名单（发送本身失败，
// 多半是临时故障）值得。
func classifySendResult(sr *dingtalk.SendResult) (retryable bool, errorCode string) {
	if sr.Success {
		return false, ""
	}
	switch sr.Reason {
	case "INVALID_USER", "FORBIDDEN_USER":
		return false, sr.Reason
	default: // "SEND_FAILED" 或以后新增的分类
		return true, sr.Reason
	}
}

func (d *deliverer) recordConfirmed(ctx context.Context, p dispatchPayload,
	success bool, errorCode string, retryable bool, taskID int64) error {
	return besdk.WithTx(ctx, d.db, d.role, d.schema, func(tx *sql.Tx) error {
		return recordResultTx(ctx, tx, d.schema, result{
			RecordID: p.RecordID, Phase: repo.PhaseConfirmed, Attempt: p.Attempt,
			Success: success, ErrorCode: errorCode, Retryable: retryable,
			ExternalTaskID: strconv.FormatInt(taskID, 10),
		})
	})
}

// result 是一个投递阶段的结果：既写进 delivery_attempts，也作为
// integration.im.result.v1 发出去。
type result struct {
	RecordID       string
	Phase          string
	Attempt        int
	Success        bool
	ErrorCode      string
	Retryable      bool
	ExternalTaskID string
}

// recordResultTx 在同一个事务里写 delivery_attempts 并把结果事件写进
// outbox（Outbox 模式：要么都在，要么都不在）。
//
// ⚠️ Event.Version = attempt*10 + phase_ordinal（ACCEPTED=1，CONFIRMED=2）。
// 同一个 record_id 在这条 subject 上的全部结果事件（跨阶段、跨重试）共用
// (subject, aggregate_id)，通知中心的 event_inbox 只收 version 严格更大的，
// version 不递增的事件会被当成重复静默丢掉。
func recordResultTx(ctx context.Context, tx *sql.Tx, schema string, res result) error {
	if err := repo.RecordDeliveryAttemptTx(ctx, tx, repo.RecordAttemptInput{
		RecordID: res.RecordID, Phase: res.Phase, Success: res.Success,
		ErrorCode: res.ErrorCode, Retryable: res.Retryable, ExternalTaskID: res.ExternalTaskID,
	}); err != nil {
		return err
	}

	phaseOrdinal := int64(1)
	if res.Phase == repo.PhaseConfirmed {
		phaseOrdinal = 2
	}
	payload, err := json.Marshal(map[string]any{
		"record_id": res.RecordID, "adapter": repo.AdapterDingtalk, "phase": res.Phase, "attempt": res.Attempt,
		"success": res.Success, "error_code": res.ErrorCode, "retryable": res.Retryable, "external_task_id": res.ExternalTaskID,
	})
	if err != nil {
		return err
	}
	return besdk.PublishOutbox(tx, schema, besdk.Event{
		Subject: resultSubject, AggregateID: res.RecordID,
		Version: int64(res.Attempt)*10 + phaseOrdinal, Payload: payload,
	})
}
