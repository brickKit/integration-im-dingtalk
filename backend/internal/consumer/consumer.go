// Package consumer 是 integration-im-dingtalk 唯一的入口（设计计划 §3：
// 没有"发消息"的 rpc，发消息只能通过事件进来）。消费族级派发事件
// infra.notification.dispatch.im.v1，先判断 target_adapters 里有没有
// "dingtalk"（channel:im 族的全部适配器订阅同一个 subject，NATS 核心是
// 广播，漏了这一步会导致装了三个 IM 通道的客户每条通知收到三遍——设计
// 计划 §4 的 ⚠️），有则解析 userid → 调钉钉 asyncsend_v2 → 记 ACCEPTED
// → 延迟回查 getsendresult → 记 CONFIRMED，两个阶段各发一条
// integration.im.result.v1。
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

	"github.com/brickKit/integration-im-dingtalk/backend/internal/dingtalk"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/repo"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/tokenmgr"
)

// confirmMaxPolls/confirmInitialDelay：延迟回查的轮数与首次等待时长
// （设计计划 §4.2 第②步："隔几秒查一次，未出结果则退避重试几轮，不引入
// 定时任务框架"）。每轮失败后等待时长翻倍（简单指数退避）。生产环境用
// 默认值；测试用注入的更短延迟，否则一条测试要跑几十秒。
const confirmMaxPolls = 4

func Start(ctx context.Context, db *sql.DB, role, schema string, nc *nats.Conn,
	r *repo.Repo, client *dingtalk.Client, tm *tokenmgr.Manager, agentID int64,
	confirmInitialDelay time.Duration, logger *slog.Logger) error {

	handler := dispatchHandler(db, role, schema, r, client, tm, agentID, confirmInitialDelay, logger)
	return besdk.Consume(ctx, nc, db, role, schema, "infra.notification.dispatch.im.v1", handler)
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

func dispatchHandler(db *sql.DB, role, schema string, r *repo.Repo, client *dingtalk.Client,
	tm *tokenmgr.Manager, agentID int64, confirmInitialDelay time.Duration, logger *slog.Logger) func(context.Context, *sql.Tx, besdk.Event) error {
	return func(ctx context.Context, tx *sql.Tx, ev besdk.Event) error {
		var p dispatchPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("解析 %s payload: %w", ev.Subject, err)
		}
		if !containsAdapter(p.TargetAdapters, repo.AdapterDingtalk) {
			// ⚠️ 不是找我的——设计计划 §4 明文要求的这一步不能省。
			return nil
		}
		if p.Attempt <= 0 {
			p.Attempt = 1 // 宽容反序列化：旧版本/异常 payload 缺这个字段时退化成"当第一次处理"
		}

		userid, err := resolveUserID(ctx, r, client, tm, p.RecipientPhone)
		if err != nil {
			return recordConfirmedFailureTx(ctx, tx, schema, r, p, err)
		}

		token, err := tm.EnsureValid(ctx)
		if err != nil {
			return recordConfirmedFailureTx(ctx, tx, schema, r, p, fmt.Errorf("获取 access_token 失败: %w", err))
		}

		taskID, err := sendWithAutoRefresh(ctx, client, tm, token, agentID, userid, p.Body)
		if err != nil {
			return recordConfirmedFailureTx(ctx, tx, schema, r, p, err)
		}

		if err := repo.RecordDeliveryAttemptTx(ctx, tx, repo.RecordAttemptInput{
			RecordID: p.RecordID, Phase: repo.PhaseAccepted, ExternalTaskID: strconv.FormatInt(taskID, 10),
		}); err != nil {
			return err
		}
		if err := publishResultTx(tx, schema, p.RecordID, repo.PhaseAccepted, p.Attempt, false, "", false, strconv.FormatInt(taskID, 10)); err != nil {
			return err
		}

		// ⚠️ 延迟回查必须在 tx 提交之后独立进行——不能在这个事务里
		// time.Sleep：那会一直占着一个数据库连接与这条消息的 inbox
		// 声明，同一条消息的其它并发处理、乃至整个消费循环的吞吐都会被
		// 拖住。用调用方传进来的长生命周期 ctx（Module.Start 的 ctx，
		// 组件关闭时会取消，goroutine 跟着退出，不会泄漏，同 §13.3
		// 铁律七"后台循环必须接 ctx"的精神）。
		go confirmLater(ctx, db, role, schema, r, client, tm, agentID, p, userid, taskID, confirmInitialDelay, logger)

		return nil
	}
}

// resolveUserID 先查缓存，缓存没有才真的调钉钉（设计计划 §2：钉钉那个
// 查询接口有频控，不能每发一条查一次）。
func resolveUserID(ctx context.Context, r *repo.Repo, client *dingtalk.Client, tm *tokenmgr.Manager, phone string) (string, error) {
	if cached, ok, err := r.GetUserID(ctx, phone); err != nil {
		return "", fmt.Errorf("查手机号缓存: %w", err)
	} else if ok {
		return cached, nil
	}

	token, err := tm.EnsureValid(ctx)
	if err != nil {
		return "", fmt.Errorf("获取 access_token 失败: %w", err)
	}
	userid, err := client.GetUserIDByMobile(ctx, token, phone)
	if err != nil {
		if dingtalk.IsTokenExpired(err) {
			// 设计计划 §4.1：access_token 过期是我自己的责任，先刷新
			// 一次再报，不消耗 infra-notification 的重试次数。
			token, rerr := tm.Refresh(ctx)
			if rerr != nil {
				return "", fmt.Errorf("刷新 access_token 失败: %w", rerr)
			}
			userid, err = client.GetUserIDByMobile(ctx, token, phone)
		}
		if err != nil {
			return "", err
		}
	}
	if err := r.SetUserID(ctx, phone, userid); err != nil {
		return "", fmt.Errorf("写手机号缓存: %w", err)
	}
	return userid, nil
}

// sendWithAutoRefresh 封装"access_token 过期先自己刷一次再报"这条分工
// （设计计划 §4.1 表格最后一行）——只在这一种错误上自动重试一次，其余
// 错误原样透传给调用方去分类。
func sendWithAutoRefresh(ctx context.Context, client *dingtalk.Client, tm *tokenmgr.Manager, token string, agentID int64, userid, content string) (int64, error) {
	taskID, err := client.SendWorkNotification(ctx, token, agentID, userid, content)
	if err != nil && dingtalk.IsTokenExpired(err) {
		newToken, rerr := tm.Refresh(ctx)
		if rerr != nil {
			return 0, fmt.Errorf("刷新 access_token 失败: %w", rerr)
		}
		return client.SendWorkNotification(ctx, newToken, agentID, userid, content)
	}
	return taskID, err
}

// recordConfirmedFailureTx：从未被钉钉受理（解析 userid 失败、拿不到
// token、asyncsend_v2 本身调用失败）——这些情况没有 ACCEPTED 阶段可言，
// 直接记一条 CONFIRMED 失败（设计计划 §4.2：只有真的提交成功才有
// ACCEPTED 中间态）。分类失败原因，必要时清缓存（设计计划 §2 的过期
// 策略）。
func recordConfirmedFailureTx(ctx context.Context, tx *sql.Tx, schema string, r *repo.Repo, p dispatchPayload, sendErr error) error {
	retryable, errorCode, evictCache := dingtalk.ClassifyError(sendErr)
	if evictCache {
		// ⚠️ 缓存清理走独立的 besdk.WithTx（DeleteUserID 内部自带事务），
		// 不复用这条消费路径的 tx——手机号缓存是性能缓存，不需要与
		// delivery_attempts 的写入原子；即使这个事务后面失败回滚，缓存
		// 已经清掉也没有正确性问题，下次重试重新解析一次而已。
		if err := r.DeleteUserID(ctx, p.RecipientPhone); err != nil {
			return fmt.Errorf("清理失效手机号缓存: %w", err)
		}
	}
	if err := repo.RecordDeliveryAttemptTx(ctx, tx, repo.RecordAttemptInput{
		RecordID: p.RecordID, Phase: repo.PhaseConfirmed, Success: false, ErrorCode: errorCode, Retryable: retryable,
	}); err != nil {
		return err
	}
	return publishResultTx(tx, schema, p.RecordID, repo.PhaseConfirmed, p.Attempt, false, errorCode, retryable, "")
}

// confirmLater 是设计计划 §4.2 第②步：延迟几秒后查 getsendresult，未
// 出结果就退避重试几轮，查到结果或轮次耗尽后记 CONFIRMED 并发结果事件。
// 独立于消费该条 dispatch 事件的事务之外运行（见 dispatchHandler 顶部
// 注释）。
func confirmLater(ctx context.Context, db *sql.DB, role, schema string, r *repo.Repo, client *dingtalk.Client,
	tm *tokenmgr.Manager, agentID int64, p dispatchPayload, userid string, taskID int64, delay time.Duration, logger *slog.Logger) {

	backoff := delay
	for i := 0; i < confirmMaxPolls; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		token, err := tm.EnsureValid(ctx)
		if err != nil {
			logger.Warn("延迟回查获取 access_token 失败，稍后重试", "record_id", p.RecordID, "error", err)
			backoff *= 2
			continue
		}
		result, err := client.GetSendResult(ctx, token, agentID, taskID)
		if err != nil {
			if dingtalk.IsTokenExpired(err) {
				if _, rerr := tm.Refresh(ctx); rerr != nil {
					logger.Warn("延迟回查刷新 access_token 失败", "record_id", p.RecordID, "error", rerr)
				}
			} else {
				logger.Warn("延迟回查 getsendresult 失败，稍后重试", "record_id", p.RecordID, "error", err)
			}
			backoff *= 2
			continue
		}
		if !result.Done {
			backoff *= 2
			continue
		}

		success, retryable, errorCode := classifySendResult(result, userid)
		if err := recordAndPublishConfirmed(ctx, db, role, schema, p, success, errorCode, retryable, taskID); err != nil {
			logger.Error("落库/发布 CONFIRMED 结果失败", "record_id", p.RecordID, "error", err)
		}
		return
	}

	// 轮次耗尽仍未出结果——当成可重试的超时处理，不能永远悬而不决
	// （设计计划 §2 ⚠️："停在 ACCEPTED 超过阈值的记录要能被查出来"，
	// 这里主动发一条 CONFIRMED 失败比让它无限期停在 ACCEPTED 更好）。
	if err := recordAndPublishConfirmed(ctx, db, role, schema, p, false, "GET_SEND_RESULT_TIMEOUT", true, taskID); err != nil {
		logger.Error("落库/发布超时结果失败", "record_id", p.RecordID, "error", err)
	}
}

// classifySendResult 把 invalid/forbidden 名单（配置/权限类问题，钉钉
// 那边"这个人本来就不该收到"）与 failed 名单（发送本身失败，更可能是
// 临时故障）区分开——前者不值得重试，后者值得（同 dingtalk.ClassifyError
// 的分工原则，这里的信息来自 getsendresult 而不是 errcode，所以单独判）。
func classifySendResult(result *dingtalk.SendResult, userid string) (success, retryable bool, errorCode string) {
	if result.Success(userid) {
		return true, false, ""
	}
	for _, u := range result.InvalidUserIDs {
		if u == userid {
			return false, false, "INVALID_USER"
		}
	}
	for _, u := range result.ForbiddenUserIDs {
		if u == userid {
			return false, false, "FORBIDDEN_USER"
		}
	}
	return false, true, "SEND_FAILED"
}

// recordAndPublishConfirmed 开自己的事务（confirmLater 跑在独立
// goroutine 里，不持有 dispatchHandler 那条消息的 tx——那条 tx 早就
// 提交了），记 CONFIRMED + 发结果事件必须在同一个事务里（Outbox
// Pattern，同 RecordDeliveryAttemptTx 顶部注释）。
func recordAndPublishConfirmed(ctx context.Context, db *sql.DB, role, schema string, p dispatchPayload,
	success bool, errorCode string, retryable bool, taskID int64) error {
	return besdk.WithTx(ctx, db, role, schema, func(tx *sql.Tx) error {
		if err := repo.RecordDeliveryAttemptTx(ctx, tx, repo.RecordAttemptInput{
			RecordID: p.RecordID, Phase: repo.PhaseConfirmed, Success: success,
			ErrorCode: errorCode, Retryable: retryable, ExternalTaskID: strconv.FormatInt(taskID, 10),
		}); err != nil {
			return err
		}
		return publishResultTx(tx, schema, p.RecordID, repo.PhaseConfirmed, p.Attempt, success, errorCode, retryable, strconv.FormatInt(taskID, 10))
	})
}

// publishResultTx 发 integration.im.result.v1（族级 subject，设计计划
// §4）。⚠️ version = attempt*10 + phase_ordinal（ACCEPTED=1，
// CONFIRMED=2）——同一个 record_id 在这条 subject 上的所有结果事件
// （跨阶段、跨重试）必须严格递增，否则 infra-notification 的
// event_inbox 会把后到的事件当重复消息吞掉（同
// infra-notification consumer.go publishIMDispatchTx 踩过的同一类
// bug，contracts/events/im-dingtalk.events.json 顶部记了这条算法）。
func publishResultTx(tx *sql.Tx, schema, recordID, phase string, attempt int, success bool, errorCode string, retryable bool, externalTaskID string) error {
	phaseOrdinal := 1
	if phase == repo.PhaseConfirmed {
		phaseOrdinal = 2
	}
	version := int64(attempt)*10 + int64(phaseOrdinal)

	payload, err := json.Marshal(map[string]any{
		"record_id": recordID, "adapter": repo.AdapterDingtalk, "phase": phase, "attempt": attempt,
		"success": success, "error_code": errorCode, "retryable": retryable, "external_task_id": externalTaskID,
	})
	if err != nil {
		return err
	}
	return besdk.PublishOutbox(tx, schema, besdk.Event{
		Subject: "integration.im.result.v1", AggregateID: recordID, Version: version, Payload: payload,
	})
}
