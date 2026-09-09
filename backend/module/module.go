// Package module 是 integration-im-dingtalk 唯一的装配入口（全局约束
// §K、设计书 §12.5.1、§13.3 铁律七）。单跑与合并走同一个 New 函数；
// 模块只交回零件，谁去 Listen、谁开池、谁 init OTel、谁装信号处理器，
// 全归调用方。
package module

import (
	"context"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
	imv1 "github.com/brickKit/integration-im-dingtalk/gen/integration/im/v1"
	"google.golang.org/grpc"

	"github.com/brickKit/integration-im-dingtalk/backend/internal/consumer"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/dingtalk"
	grpcapi "github.com/brickKit/integration-im-dingtalk/backend/internal/grpc"
	httpapi "github.com/brickKit/integration-im-dingtalk/backend/internal/http"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/partition"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/repo"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/service"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/tokenmgr"
	"github.com/brickKit/integration-im-dingtalk/migrations"
)

// confirmInitialDelay：延迟回查 getsendresult 前先等这么久（设计计划
// §4.2 第②步："隔几秒查一次"）。不做成 configSchema 项——这是内部实现
// 细节，不是装配期该关心的旋钮（同 SOP-P 判据：没有已知需求支撑的可
// 配置是提前抽象）。
const confirmInitialDelay = 5 * time.Second

// New 构造 integration-im-dingtalk 模块。签名一个字都不许改（§12.5.1）。
func New(ctx context.Context, rt *besdk.Runtime) (*besdk.Module, error) {
	// ⚠️ 配置只从 rt.Config 来，模块里零 os.Getenv（§12.5.3、决策 110）。
	schema := rt.Config.StringOr("pgSchema", "integration_im_dingtalk")
	role := schema + "_rw"

	dingtalkBaseURL := rt.Config.StringOr("dingtalkBaseUrl", "https://oapi.dingtalk.com")
	appKey := rt.Config.MustString("dingtalkAppKey")
	appSecret := rt.Config.MustString("dingtalkAppSecret")
	agentID, ok := rt.Config.Int("dingtalkAgentId")
	if !ok {
		panic("必填配置项 \"dingtalkAgentId\" 未注入或不是合法整数")
	}

	// ⚠️ 池从 rt.DB 来，不许自己 sql.Open（§13.3 铁律二）。
	r := repo.New(rt.DB, role, schema)
	client := dingtalk.NewClient(dingtalkBaseURL, appKey, appSecret)
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

		Migrations: migrations.FS, // 合并态由外壳按拓扑顺序跑（§13.3 铁律五）

		// 后台循环：Outbox 推送 + 周分区维护（event_outbox/event_inbox）+
		// 月分区维护（delivery_attempts）+ 消费族级派发事件。四个循环
		// 必须并发跑，不能顺序调用。
		Start: func(ctx context.Context) error {
			errCh := make(chan error, 3)
			go func() { errCh <- besdk.StartOutboxPump(ctx, rt.DB, schema, rt.NATS, rt.Logger) }()
			go func() { errCh <- partition.Start(ctx, rt.DB, role, schema, rt.Logger) }()
			go func() {
				errCh <- consumer.Start(ctx, rt.DB, role, schema, rt.NATS, r, client, tm, int64(agentID), confirmInitialDelay, rt.Logger)
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
