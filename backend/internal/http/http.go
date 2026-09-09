// Package http 是 integration-im-dingtalk 的 REST 面（族级前缀
// /integration/im，不是 /integration/im-dingtalk——设计计划 §3）。两个
// 端点都是排障/应急用，权限键统一 integration.im.admin（同
// assembly.yaml 里两行permissions 共用一个 key 的设计）。
package http

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/repo"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/service"
)

func RegisterRoutes(eng *gin.Engine, svc *service.Service) {
	g := eng.Group("/integration/im")
	besdk.GET(g, "/admin/deliveries", "integration.im.admin", listDeliveriesHandler(svc))
	besdk.POST(g, "/admin/token/refresh", "integration.im.admin", refreshTokenHandler(svc))
}

const rfc3339 = "2006-01-02T15:04:05.999999999Z07:00"

func toDeliveryDTO(d *repo.DeliveryAttempt) gin.H {
	return gin.H{
		"record_id": d.RecordID, "phase": d.Phase, "success": d.Success,
		"error_code": d.ErrorCode, "retryable": d.Retryable, "external_task_id": d.ExternalTaskID,
		"attempted_at": d.AttemptedAt.Format(rfc3339),
	}
}

func listDeliveriesHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		pageSize, _ := strconv.Atoi(c.Query("page_size"))
		attempts, nextCursor, err := svc.ListDeliveries(c.Request.Context(), repo.ListInput{
			RecordID: c.Query("record_id"), Phase: c.Query("phase"),
			Cursor: c.Query("cursor"), PageSize: int32(pageSize),
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		dtos := make([]gin.H, 0, len(attempts))
		for _, d := range attempts {
			dtos = append(dtos, toDeliveryDTO(d))
		}
		c.JSON(http.StatusOK, gin.H{"attempts": dtos, "next_cursor": nextCursor})
	}
}

func refreshTokenHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		expiresAt, err := svc.RefreshToken(c.Request.Context())
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, gin.H{"expires_at": expiresAt.Format(rfc3339)})
	}
}
