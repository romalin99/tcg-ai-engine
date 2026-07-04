// 风控规则引擎服务入口。
//
// 启动流程（对齐 tcg-ucs-fe 的 cmd/api 风格）：
//
//	flag 解析 → 加载配置 → 初始化日志 → 构建规则数据源（file/oracle）
//	→ 首次全量加载规则（失败即退出，fail-fast）
//	→ 启动热更新轮询 → 注册路由 → 启动 HTTP → 信号驱动优雅停机
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gofiber/fiber/v3"
	gruleast "github.com/hyperjumptech/grule-rule-engine/ast"
	"go.uber.org/zap"

	"tcg-ai-engine/internal/config"
	"tcg-ai-engine/internal/engine"
	"tcg-ai-engine/internal/handler"
	"tcg-ai-engine/internal/repository"
	"tcg-ai-engine/internal/router"
	"tcg-ai-engine/internal/service"
	"tcg-ai-engine/pkg/logs"
	"tcg-ai-engine/pkg/oracle"
)

var configFile = flag.String("f", "./config/config.toml", "配置文件路径")

// @title           TCG-AI-ENGINE API
// @version         1.0
// @description     电商风控规则引擎服务（grule），支持规则热更新
// @BasePath        /
func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(*configFile)
	if err != nil {
		return err
	}

	logger, err := logs.New(cfg.Log.Level)
	if err != nil {
		return err
	}
	defer logger.Sync() //nolint:errcheck

	// GRL 里的 Log()/LogFormat() 走 ast 包的全局 logger，必须用 ast.SetLogger 注入
	gruleast.SetLogger(logger)

	// 规则数据源：默认 file（rules/ 目录），可切 oracle
	repo, db, err := buildRepository(cfg)
	if err != nil {
		return err
	}
	if db != nil {
		defer db.Close()
	}

	eng := engine.New(logger, cfg.Log.GRLTrace)
	reloader := engine.NewReloader(repo, eng, cfg.ReloadInterval(), logger)

	// 首次加载 fail-fast：规则都装不进来，服务起来也没有意义
	info, _, err := reloader.ReloadOnce(context.Background())
	if err != nil {
		return fmt.Errorf("首次加载规则失败: %w", err)
	}
	logger.Info("规则加载完成",
		zap.String("source", info.Source),
		zap.Int("rule_count", info.RuleCount),
		zap.String("checksum", info.Checksum[:12]))

	// 后台热更新轮询：改规则 → 下个周期自动生效，无需重启
	reloader.Start()
	defer reloader.Stop()

	riskSvc := service.NewRiskService(eng, logger)
	riskHandler := handler.NewRisk(riskSvc, eng, logger)
	rulesHandler := handler.NewRules(eng, reloader, logger)

	app := router.New(cfg, riskHandler, rulesHandler, logger)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP 服务启动", zap.String("addr", cfg.Server.Addr))
		// 优雅停机时 Listen 返回 nil，不会误报到 errCh
		if err := app.Listen(cfg.Server.Addr, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
			errCh <- err
		}
	}()

	// 优雅停机：等在途请求跑完再退（规则执行都在请求生命周期内）
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		logger.Info("收到退出信号，开始优雅停机", zap.String("signal", sig.String()))
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout())
	defer cancel()
	if err := app.ShutdownWithContext(ctx); err != nil {
		return fmt.Errorf("优雅停机超时: %w", err)
	}
	logger.Info("服务已退出")
	return nil
}

// buildRepository 按配置构建规则数据源；oracle 源同时返回连接池供关闭
func buildRepository(cfg *config.Config) (repository.RuleRepository, *sql.DB, error) {
	switch cfg.Rules.Source {
	case "oracle":
		db, err := oracle.Open(cfg.Rules.Oracle.DSN)
		if err != nil {
			return nil, nil, err
		}
		repo, err := repository.NewOracleRepository(db, cfg.Rules.Oracle.Table)
		if err != nil {
			db.Close()
			return nil, nil, err
		}
		return repo, db, nil
	default: // config.Load 已校验，这里只剩 file
		return repository.NewFileRepository(cfg.Rules.File.Dir), nil, nil
	}
}
