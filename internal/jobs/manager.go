package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"arcproof/internal/app"
)

type Manager struct {
	service        *app.Service
	logger         *slog.Logger
	reviewInterval time.Duration
	expiryInterval time.Duration
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

func New(service *app.Service, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{service: service, logger: logger, reviewInterval: 2 * time.Second, expiryInterval: 30 * time.Second}
}
func (m *Manager) WithIntervals(review, expiry time.Duration) *Manager {
	if review > 0 {
		m.reviewInterval = review
	}
	if expiry > 0 {
		m.expiryInterval = expiry
	}
	return m
}
func (m *Manager) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	m.wg.Add(2)
	go m.reviewLoop(ctx)
	go m.expiryLoop(ctx)
}
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}
func (m *Manager) reviewLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.reviewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, err := m.service.RunDueTasks("system:review-worker", 20)
			if err != nil {
				m.logger.Error("复核任务执行失败", "error", err)
			} else if count > 0 {
				m.logger.Info("复核任务已处理", "count", count)
			}
		}
	}
}
func (m *Manager) expiryLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.expiryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, err := m.service.ScanExpiredEvidence("system:expiry-scanner")
			if err != nil {
				m.logger.Error("证据到期扫描失败", "error", err)
			} else if count > 0 {
				m.logger.Info("发现到期证据", "count", count)
			}
		}
	}
}
