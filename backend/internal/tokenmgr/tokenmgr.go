// Package tokenmgr 管理钉钉 access_token 的缓存与刷新——被 consumer
// （发消息前要拿一个有效 token）与 service（GetChannelHealth/管理员手动
// 刷新）两边共用，避免同一套"缓存够不够新、要不要刷新"的判断写两遍。
package tokenmgr

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/brickKit/integration-im-dingtalk/backend/internal/dingtalk"
	"github.com/brickKit/integration-im-dingtalk/backend/internal/repo"
)

// expiryBuffer：提前这么久就当作"快过期了"，主动刷新而不是等真正过期
// 那一刻——钉钉的 access_token 有效期通常是 2 小时，留 5 分钟余量避免
// "刚判断没过期，下一秒调用就因为过期失败"的边界竞态。
const expiryBuffer = 5 * time.Minute

type Manager struct {
	repo   *repo.Repo
	client *dingtalk.Client

	// mu 只保护"要不要发起一次刷新请求"这个决策，不是保护 DB——多个
	// goroutine 同时判断"token 快过期了"时，只让一个真的去调钉钉
	// gettoken，其余等它结果，省得每条并发消息各刷新一次撞频控。
	mu sync.Mutex
}

func New(r *repo.Repo, client *dingtalk.Client) *Manager {
	return &Manager{repo: r, client: client}
}

func isValid(t repo.Token) bool {
	return t.AccessToken != "" && time.Now().Before(t.ExpiresAt.Add(-expiryBuffer))
}

// EnsureValid 返回一个可用的 access_token：缓存没过期（留了
// expiryBuffer 余量）就直接用缓存，否则真的调钉钉刷新一次并落库。
//
// ⚠️ 并发去重（single-flight）只属于这个函数，不属于 Refresh——Refresh
// 的契约是"无条件真的调一次钉钉"（应急强制刷新的语义），如果 Refresh
// 自己也在拿到锁后"发现缓存还没过期就跳过调用"，会让"强制刷新"变成
// 有时候不刷新，这是写第一版时发现的真实语义错误：管理员点了
// POST /admin/token/refresh 却因为缓存恰好没过期，实际什么都没做。
func (m *Manager) EnsureValid(ctx context.Context) (string, error) {
	cached, err := m.repo.GetToken(ctx)
	if err != nil {
		return "", fmt.Errorf("读 token 缓存: %w", err)
	}
	if isValid(cached) {
		return cached.AccessToken, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// 拿到锁之后再读一次缓存——如果刚才等锁的这段时间里，另一个 goroutine
	// 已经刷新过了，直接用它的结果，不重复调钉钉（省得并发消息各刷新
	// 一次撞频控，见 mu 字段注释）。这个"重新检查"只在这里做，Refresh
	// 本身永远不做。
	cached, err = m.repo.GetToken(ctx)
	if err != nil {
		return "", fmt.Errorf("读 token 缓存: %w", err)
	}
	if isValid(cached) {
		return cached.AccessToken, nil
	}
	return m.doRefresh(ctx)
}

// Refresh 无条件调钉钉重新申请一次 token 并落库，不管缓存是否还有效
// ——供 POST /admin/token/refresh（应急强制刷新）与"access_token 过期，
// 我先自己刷一次再报"那条分工（设计计划 §4.1）直接调用。
func (m *Manager) Refresh(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.doRefresh(ctx)
}

// doRefresh 是真正调钉钉 + 落库的那一步，调用方必须已经持有 m.mu。
func (m *Manager) doRefresh(ctx context.Context) (string, error) {
	token, ttl, err := m.client.FetchAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("申请 access_token 失败: %w", err)
	}
	expiresAt := time.Now().Add(ttl)
	if err := m.repo.SetToken(ctx, token, expiresAt); err != nil {
		return "", fmt.Errorf("落库 access_token 失败: %w", err)
	}
	return token, nil
}

// CurrentExpiry 供 GetChannelHealth 展示"token 还有多久过期"，不触发
// 任何刷新（纯读，运维排障用）。
func (m *Manager) CurrentExpiry(ctx context.Context) (time.Time, error) {
	cached, err := m.repo.GetToken(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return cached.ExpiresAt, nil
}
