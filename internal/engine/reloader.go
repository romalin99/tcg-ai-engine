package engine

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"tcg-ai-engine/internal/repository"
)

// Reloader 规则热更新器：定时从数据源拉取全量规则，交给 Engine.Reload。
// 内容指纹未变化时 Reload 是空操作，所以轮询的代价只是读几个小文件/一条 SQL。
//
// 文件源和 Oracle 源走同一条轮询链路，无需 fsnotify 之类的平台相关机制；
// POST /api/v1/rules/reload 也是直接调 ReloadOnce，手动与自动共用一套逻辑。
type Reloader struct {
	repo     repository.RuleRepository
	eng      *Engine
	interval time.Duration
	logger   *zap.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewReloader interval <= 0 表示不启动后台轮询（仍可手动 ReloadOnce）
func NewReloader(repo repository.RuleRepository, eng *Engine, interval time.Duration, logger *zap.Logger) *Reloader {
	return &Reloader{repo: repo, eng: eng, interval: interval, logger: logger}
}

// ReloadOnce 立即执行一次"拉取→比对→重建→切换"。
// changed=false 表示规则内容没变。加载或构建失败时旧版本继续生效。
func (r *Reloader) ReloadOnce(ctx context.Context) (Info, bool, error) {
	rules, err := r.repo.Load(ctx)
	if err != nil {
		return Info{}, false, err
	}
	return r.eng.Reload(rules, r.repo.Source())
}

// Start 启动后台轮询 goroutine
func (r *Reloader) Start() {
	if r.interval <= 0 {
		r.logger.Info("规则自动热更新已禁用（reload_interval_seconds <= 0）")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Go(func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		r.logger.Info("规则热更新轮询启动",
			zap.Duration("interval", r.interval),
			zap.String("source", r.repo.Source()))
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				info, changed, err := r.ReloadOnce(ctx)
				switch {
				case err != nil:
					// 只告警不切换，当前版本继续服务
					r.logger.Error("规则热更新失败，沿用当前版本", zap.Error(err))
				case changed:
					r.logger.Info("规则热更新生效",
						zap.String("checksum", info.Checksum[:12]),
						zap.Int("rule_count", info.RuleCount))
				}
			}
		}
	})
}

// Stop 停止后台轮询并等待退出
func (r *Reloader) Stop() {
	if r.cancel != nil {
		r.cancel()
		r.wg.Wait()
	}
}
