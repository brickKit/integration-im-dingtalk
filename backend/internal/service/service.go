// Package service 是 REST 与 gRPC 共用的编排层，只有读与排障操作
// （BatchGetDeliveryStatus / ListDeliveries / GetChannelHealth /
// RefreshToken）。发消息不经过这里：唯一入口是 consumer 包消费的派发事件。
package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/repo"
	"github.com/brickKit/integration-im-dingtalk/v2/backend/internal/tokenmgr"
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

// ChannelHealth 是 GetChannelHealth 的返回值：给运维看，不给业务判断用。
type ChannelHealth struct {
	Reachable      bool
	TokenExpiresAt time.Time
	LastError      string
}

// GetChannelHealth 试着拿一个有效 token，以此判断钉钉这条通道现在通不通。
// 这是本组件唯一探测外部依赖的地方：它是显式的排障动作，不是 /healthz
// （/healthz 只报本进程活着，钉钉一抖就让平台重启本组件是错的）。
func (s *Service) GetChannelHealth(ctx context.Context) *ChannelHealth {
	token, err := s.tm.EnsureValid(ctx)
	if err != nil {
		return &ChannelHealth{Reachable: false, LastError: err.Error()}
	}
	_ = token
	expiresAt, _ := s.tm.CurrentExpiry(ctx)
	return &ChannelHealth{Reachable: true, TokenExpiresAt: expiresAt}
}

// RefreshToken 供 POST /admin/token/refresh 应急手动强制刷新。
func (s *Service) RefreshToken(ctx context.Context) (time.Time, error) {
	if _, err := s.tm.Refresh(ctx); err != nil {
		return time.Time{}, err
	}
	return s.tm.CurrentExpiry(ctx)
}
