// Package grpc 实现 integration.im.v1.ImChannelService——族级 gRPC 契约
// （设计计划 §3：包名是族级 integration.im，五个 channel:im 适配器实现
// 同一份 proto）。
package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	imv1 "github.com/brickKit/integration-im-dingtalk/gen/integration/im/v1"

	"github.com/brickKit/integration-im-dingtalk/backend/internal/repo"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/service"
)

type server struct {
	imv1.UnimplementedImChannelServiceServer
	svc *service.Service
}

func New(svc *service.Service) imv1.ImChannelServiceServer {
	return &server{svc: svc}
}

func toProtoPhase(phase string) imv1.DeliveryPhase {
	switch phase {
	case repo.PhaseAccepted:
		return imv1.DeliveryPhase_DELIVERY_PHASE_ACCEPTED
	case repo.PhaseConfirmed:
		return imv1.DeliveryPhase_DELIVERY_PHASE_CONFIRMED
	default:
		return imv1.DeliveryPhase_DELIVERY_PHASE_UNSPECIFIED
	}
}

func toProtoStatus(d *repo.DeliveryAttempt) *imv1.DeliveryStatus {
	return &imv1.DeliveryStatus{
		RecordId: d.RecordID, Adapter: d.Adapter, Phase: toProtoPhase(d.Phase),
		Success: d.Success, ErrorCode: d.ErrorCode, Retryable: d.Retryable,
		ExternalTaskId: d.ExternalTaskID, UpdatedAt: timestamppb.New(d.AttemptedAt),
	}
}

func (s *server) BatchGetDeliveryStatus(ctx context.Context, req *imv1.BatchGetDeliveryStatusRequest) (*imv1.BatchGetDeliveryStatusResponse, error) {
	attempts, err := s.svc.BatchGetDeliveryStatus(ctx, req.RecordIds)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	out := make([]*imv1.DeliveryStatus, 0, len(attempts))
	for _, d := range attempts {
		out = append(out, toProtoStatus(d))
	}
	return &imv1.BatchGetDeliveryStatusResponse{Statuses: out}, nil
}

func (s *server) GetChannelHealth(ctx context.Context, req *imv1.GetChannelHealthRequest) (*imv1.ChannelHealth, error) {
	h := s.svc.GetChannelHealth(ctx)
	health := &imv1.ChannelHealth{Reachable: h.Reachable, LastError: h.LastError}
	if !h.TokenExpiresAt.IsZero() {
		health.TokenExpiresAt = timestamppb.New(h.TokenExpiresAt)
	}
	return health, nil
}
