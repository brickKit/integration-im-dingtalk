// Package module 是 integration-im-dingtalk 唯一的装配入口。独立运行
// （cmd/server 的 besdk.RunStandalone）与进外壳走同一个 New：模块只交回零件
// （HTTP handler、gRPC 注册函数、后台循环），谁去 Listen、谁开连接池、谁初始化
// OTel 与信号处理，全归调用方——同一份代码进外壳之后不会与别的成员互相覆盖。
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

// confirmInitialDelay：钉钉受理后，第一次回查 getsendresult 前先等这么久。
// 不做成配置项：这是内部实现细节，没有部署方需要调它。
const confirmInitialDelay = 5 * time.Second

// New 构造 integration-im-dingtalk 模块。签名是外壳与 RunStandalone 共同依赖
// 的约定，不改。
func New(ctx context.Context, rt *besdk.Runtime) (*besdk.Module, error) {
	// 配置只从 rt.Config 读，模块里不碰 os.Getenv：一个进程只有一份环境，
	// 进外壳后各成员的 PG_SCHEMA 会互相覆盖，不报错。
	schema := rt.Config.StringOr("PG_SCHEMA", "integration_im_dingtalk")
	// role 是每个事务里 SET LOCAL ROLE 的目标：独立运行时它就是登录角色；
	// 进外壳后外壳以自己的角色登录，靠这一步切到本组件的角色。
	role := schema + "_rw"

	dt, err := readDingtalkConfig(rt.Config)
	if err != nil {
		return nil, err
	}

	// 连接池、日志都从 rt 来，不自己 sql.Open：进外壳后全体成员共用外壳的池。
	r := repo.New(rt.DB, role, schema)
	client := dingtalk.NewClient(dt.baseURL, dt.appKey, dt.appSecret)
	tm := tokenmgr.New(r, client)
	svc := service.New(r, tm, rt.Logger)

	// engine 必须用 besdk.NewGinEngine：它挂好了 OTel、request-id、错误到状态码
	// 的映射、脱敏访问日志、RED 指标、/healthz 与 /metrics；gin.New() 会把这些
	// 全部悄悄丢掉。
	eng := besdk.NewGinEngine(rt)
	httpapi.RegisterRoutes(eng, svc)

	return &besdk.Module{
		HTTPHandler: eng,

		// gRPC 由调用方在 extraPorts["grpc"] 上 Listen，模块只注册服务。
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
				return err // 返回 error，不 log.Fatal：进外壳后一个成员退进程，同壳成员一起下线
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
