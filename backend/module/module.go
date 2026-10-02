// Package module 是 integration-im-dingtalk 唯一的装配入口（全局约束
// §K、设计书 §12.5.1、§13.3 铁律七）。单跑与合并走同一个 New 函数；
// 模块只交回零件，谁去 Listen、谁开池、谁 init OTel、谁装信号处理器，
// 全归调用方。
package module

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
	imv1 "github.com/brickKit/integration-im-dingtalk/gen/integration/im/v1"
	"google.golang.org/grpc"

	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/consumer"
	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/dingtalk"
	grpcapi "github.com/brickKit/integration-im-dingtalk/v2/backend/internal/grpc"
	httpapi "github.com/brickKit/integration-im-dingtalk/v2/backend/internal/http"
	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/partition"
	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/repo"
	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/service"
	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/tokenmgr"
)

// confirmInitialDelay：延迟回查 getsendresult 前先等这么久（设计计划
// §4.2 第②步："隔几秒查一次"）。不做成 configSchema 项——这是内部实现
// 细节，不是装配期该关心的旋钮（同 SOP-P 判据：没有已知需求支撑的可
// 配置是提前抽象）。
const confirmInitialDelay = 5 * time.Second

// New 构造 integration-im-dingtalk 模块。签名一个字都不许改（§12.5.1）。
func New(ctx context.Context, rt *besdk.Runtime) (*besdk.Module, error) {
	// ⚠️ 配置只从 rt.Config 来，模块里零 os.Getenv（§12.5.3、决策 110）。
	schema := rt.Config.StringOr("PG_SCHEMA", "integration_im_dingtalk")
	role := schema + "_rw"

	dt, err := readDingtalkConfig(rt.Config)
	if err != nil {
		return nil, err
	}

	// ⚠️ 池从 rt.DB 来，不许自己 sql.Open（§13.3 铁律二）。
	r := repo.New(rt.DB, role, schema)
	client := dingtalk.NewClient(dt.baseURL, dt.appKey, dt.appSecret)
	tm := tokenmgr.New(r, client)
	svc := service.New(r, tm, rt.Logger)

	// HTTP：engine 必须用 besdk.NewGinEngine，它已挂好 OTel / request-id /
	// error→status / PII 脱敏日志 / RED 指标 / /healthz / /metrics。
	eng := besdk.NewGinEngine(rt)
	httpapi.RegisterRoutes(eng, svc)

	return &besdk.Module{
		HTTPHandler: eng,

		// ⚠️ gRPC 一个不省，而且由调用方在 extraPorts["grpc"] 上 Listen
		// （§1.5 原则一）。
		RegisterGRPC: func(gs *grpc.Server) {
			imv1.RegisterImChannelServiceServer(gs, grpcapi.New(svc))
		},

		// 三个后台循环并发跑（每个都阻塞到 ctx 取消，不能顺序调用）：Outbox
		// 推送、分区维护（outbox / inbox 的周分区与 delivery_attempts 的月
		// 分区）、消费族级派发事件。
		Start: func(ctx context.Context) error {
			errCh := make(chan error, 3)
			go func() { errCh <- besdk.StartOutboxPump(ctx, rt.DB, schema, rt.NATS, rt.Logger) }()
			go func() { errCh <- partition.Start(ctx, rt.DB, role, schema, rt.Logger) }()
			go func() {
				errCh <- consumer.Start(ctx, rt.DB, role, schema, rt.NATS, r, client, tm, dt.agentID, confirmInitialDelay, rt.Logger)
			}()

			select {
			case <-ctx.Done():
				return nil
			case err := <-errCh:
				return err // ⚠️ 返回 error，不许 log.Fatal：一个模块退进程 = 整组组件一起没了
			}
		},
		Stop: func(ctx context.Context) error { return nil }, // 后台循环靠 ctx 退出
	}, nil
}

type dingtalkConfig struct {
	baseURL, appKey, appSecret string
	agentID                    int64
}

// readDingtalkConfig 读钉钉应用的配置。缺键或 DINGTALK_AGENT_ID 不是整数时
// 返回 error（一次点名全部有问题的键），不 panic：进了外壳，构造函数的
// error 由外壳点名报出并按失败契约退出，panic 会让整个外壳带着堆栈崩掉。
func readDingtalkConfig(cfg besdk.Config) (dingtalkConfig, error) {
	c := dingtalkConfig{baseURL: cfg.StringOr("DINGTALK_BASE_URL", "https://oapi.dingtalk.com")}
	var problems []string
	var ok bool
	if c.appKey, ok = cfg.String("DINGTALK_APP_KEY"); !ok {
		problems = append(problems, "DINGTALK_APP_KEY 未注入")
	}
	if c.appSecret, ok = cfg.String("DINGTALK_APP_SECRET"); !ok {
		problems = append(problems, "DINGTALK_APP_SECRET 未注入")
	}
	if raw, present := cfg.String("DINGTALK_AGENT_ID"); !present {
		problems = append(problems, "DINGTALK_AGENT_ID 未注入")
	} else if n, err := strconv.ParseInt(raw, 10, 64); err != nil {
		problems = append(problems, fmt.Sprintf("DINGTALK_AGENT_ID 不是整数（%q）", raw))
	} else {
		c.agentID = n
	}
	if len(problems) > 0 {
		return dingtalkConfig{}, fmt.Errorf("钉钉配置不完整：%s", strings.Join(problems, "；"))
	}
	return c, nil
}
