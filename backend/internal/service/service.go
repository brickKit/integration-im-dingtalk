// Package service 是 integration-im-dingtalk 的 REST/gRPC 共用编排层——
// 只有读与排障操作（BatchGetDeliveryStatus/ListDeliveries/
// GetChannelHealth/RefreshToken），没有任何"发消息"的入口（设计计划
// §3：发消息只能通过事件进来，consumer 包直接编排 repo+dingtalk 客户端，
// 不经过这一层）。
package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/brickKit/integration-im-dingtalk/backend/internal/repo"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/tokenmgr"
)

type Service struct {
	repo   *repo.Repo
	tm     *tokenmgr.Manager
	logger *slog.Logger
}

func New(r *repo.Repo, tm *tokenmgr.Manager, logger *slog.Logger) *Service {
	return &Service{repo: r, tm: tm, logger: logger}
}

func (s *Service) BatchGetDeliveryStatus(ctx context.Context, recordIDs []string) ([]*repo.DeliveryAttempt, error) {
	return s.repo.BatchGetLatestStatus(ctx, recordIDs)
}

func (s *Service) ListDeliveries(ctx context.Context, in repo.ListInput) ([]*repo.DeliveryAttempt, string, error) {
	return s.repo.ListDeliveries(ctx, in)
}

// ChannelHealth 是 GetChannelHealth 的返回值——给运维看，不给业务判断用
// （设计计划 §3）。
type ChannelHealth struct {
	Reachable      bool
	TokenExpiresAt time.Time
	LastError      string
}

// GetChannelHealth 试着确保一个有效 token 来判断"钉钉这条通道现在通不
// 通"——⚠️ 这是本组件唯一允许"探测外部依赖"的地方：它是给运维看的排障
// 端点，不是 /healthz（§12.3.6 禁止把依赖的可用性写进健康检查，这里是
// 完全不同的、显式的排障动作）。
func (s *Service) GetChannelHealth(ctx context.Context) *ChannelHealth {
	token, err := s.tm.EnsureValid(ctx)
	if err != nil {
		return &ChannelHealth{Reachable: false, LastError: err.Error()}
	}
	_ = token
	expiresAt, _ := s.tm.CurrentExpiry(ctx)
	return &ChannelHealth{Reachable: true, TokenExpiresAt: expiresAt}
}

// RefreshToken 供 POST /admin/token/refresh 应急手动强制刷新
// （设计计划 §3）。
func (s *Service) RefreshToken(ctx context.Context) (time.Time, error) {
	if _, err := s.tm.Refresh(ctx); err != nil {
		return time.Time{}, err
	}
	return s.tm.CurrentExpiry(ctx)
}
