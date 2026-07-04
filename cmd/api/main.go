// 风控规则引擎服务入口：
//
//	viper 配置（ENV 选择 config/{dev,sit,prod}.toml，-f 可覆盖）
//	→ 日志（logs 单例，*zap.Logger 桥接给 internal/* 与 grule）
//	→ metrics / telemetry → 规则数据源（file/oracle）→ 首次全量加载（fail-fast）
//	→ 热更新轮询 → 可选 kafka producer / pprof / memstats
//	→ fiber（sonic JSON + cors/recover/otel/行为日志/访问日志）
//	→ 路由（业务 + prometheus + swagger）→ 信号驱动分级优雅停机
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof" // pprof 路由挂到 DefaultServeMux，由独立 pprof server 暴露
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/gofiber/fiber/v3/middleware/recover"
	gruleast "github.com/hyperjumptech/grule-rule-engine/ast"
	"go.uber.org/zap"

	"tcg-ai-engine/internal/config"
	"tcg-ai-engine/internal/engine"
	"tcg-ai-engine/internal/handler"
	"tcg-ai-engine/internal/middleware"
	"tcg-ai-engine/internal/repository"
	"tcg-ai-engine/internal/router"
	"tcg-ai-engine/internal/service"
	"tcg-ai-engine/pkg/gos"
	"tcg-ai-engine/pkg/kafka"
	"tcg-ai-engine/pkg/logs"
	"tcg-ai-engine/pkg/memstatus"
	"tcg-ai-engine/pkg/metrics"
)

var configFile = flag.String("f", "", "配置文件路径；留空按 ENV(dev/sit/prod，默认 prod) 搜索 config/{env}.toml")

func init() {
	cores := runtime.NumCPU()
	runtime.GOMAXPROCS(int(float64(cores)*5 + 0.5))
}

// ── Application container ─────────────────────────────────────────────────────

// application 汇总运行期组件，供路由注册与优雅停机按序释放。
type application struct {
	zapLogger     *zap.Logger
	reloader      *engine.Reloader
	kafkaProducer *kafka.Producer
	pprofServer   *http.Server
	stopMemStats  context.CancelFunc
	riskHandler   *handler.Risk
	rulesHandler  *handler.Rules
}

// newApplication 按依赖序初始化：
// 日志 → metrics → telemetry → 规则数据源 → 引擎/热更新 → 服务/处理器 → 可选组件。
func newApplication(cfg *config.Config) (*application, error) {
	ctx := context.Background()

	appLogger := cfg.InitLog()
	zapLogger := appLogger.Zap()
	if zapLogger == nil {
		return nil, errors.New("日志初始化失败（检查 [log] 的 mode/path 配置）")
	}
	// GRL 里的 Log()/LogFormat() 走 ast 包的全局 logger，必须用 ast.SetLogger 注入
	gruleast.SetLogger(zapLogger)

	metrics.Init(cfg.Log.ServiceName)
	cfg.TracerProvider = cfg.Telemetry.InitTracer() // Enabled=false 时内部为 no-op

	// 规则数据源：默认 file（rules/ 目录），可切 oracle
	repo, err := buildRepository(cfg)
	if err != nil {
		return nil, err
	}

	eng := engine.New(zapLogger, cfg.Rules.GRLTrace)
	reloader := engine.NewReloader(repo, eng, cfg.ReloadInterval(), zapLogger)

	// 首次加载 fail-fast：规则都装不进来，服务起来也没有意义
	info, _, err := reloader.ReloadOnce(ctx)
	if err != nil {
		return nil, fmt.Errorf("首次加载规则失败: %w", err)
	}
	logs.Info(ctx, "规则加载完成 source=%s rule_count=%d checksum=%s",
		info.Source, info.RuleCount, info.Checksum[:12])

	// 后台热更新轮询：改规则 → 下个周期自动生效，无需重启
	reloader.Start()

	riskSvc := service.NewRiskService(eng, zapLogger)
	riskHandler := handler.NewRisk(riskSvc, eng, zapLogger)
	rulesHandler := handler.NewRules(eng, reloader, zapLogger)

	// kafka producer：[kafka.producer] 配置了 brokers 才会创建（kgo 懒连接，
	// 本地 broker 不可达不影响启动）；未配置返回 nil。
	kafkaProducer := cfg.Kafka.InitProducer()
	if kafkaProducer != nil {
		logs.Info(ctx, "kafka producer 已就绪 brokers=%v topic=%s",
			cfg.Kafka.Producer.Brokers, cfg.Kafka.Producer.Topic)
	}

	// memstats 周期日志（可取消，优雅停机时停止）
	memStatsCtx, stopMemStats := context.WithCancel(context.Background())
	gos.GoSafe(func() { memstatus.MemStats(memStatsCtx) })

	return &application{
		zapLogger:     zapLogger,
		reloader:      reloader,
		kafkaProducer: kafkaProducer,
		stopMemStats:  stopMemStats,
		riskHandler:   riskHandler,
		rulesHandler:  rulesHandler,
	}, nil
}

// buildRepository 按配置构建规则数据源。
// oracle 源复用 [oracle] 段的 godror 连接池（连接/ping 失败时 Init 内部 Fatalf，
// fail-fast）；池的生命周期由 cfg.Close() 统一管理。
func buildRepository(cfg *config.Config) (repository.RuleRepository, error) {
	switch cfg.Rules.Source {
	case "oracle":
		dbx := cfg.OracleIns.Init()
		return repository.NewOracleRepository(dbx.DB, cfg.Rules.Oracle.Table)
	default: // config.Init 已校验，这里只剩 file
		return repository.NewFileRepository(cfg.Rules.File.Dir), nil
	}
}

// ── Server lifecycle ──────────────────────────────────────────────────────────

// shutdownComponents 按序释放运行期组件（在优雅停机 goroutine 内调用）：
// HTTP 入口先停（不再进新请求）→ 后台任务 → 外部连接 → 日志最后落盘。
func (a *application) shutdownComponents(ctx context.Context, shutdownCtx context.Context, fiberApp *fiber.App, cfg *config.Config) {
	logs.Info(ctx, "Shutting down Fiber server...")
	if err := fiberApp.ShutdownWithContext(shutdownCtx); err != nil {
		logs.Err(ctx, "Failed to shutdown Fiber server: %v", err)
	} else {
		logs.Info(ctx, "Fiber server has been shut down")
	}

	if a.pprofServer != nil {
		logs.Info(ctx, "Shutting down pprof server...")
		if err := a.pprofServer.Shutdown(shutdownCtx); err != nil {
			logs.Err(ctx, "Failed to shutdown pprof server: %v", err)
		}
	}

	if a.reloader != nil {
		logs.Info(ctx, "Stopping rules reloader...")
		a.reloader.Stop()
		logs.Info(ctx, "Rules reloader stopped")
	}

	if a.kafkaProducer != nil {
		logs.Info(ctx, "Closing kafka producer...")
		a.kafkaProducer.Close()
		logs.Info(ctx, "Kafka producer closed")
	}

	if a.stopMemStats != nil {
		a.stopMemStats()
	}

	gos.ReleasePool()

	// 规则 Oracle 源的 godror 连接池由 cfg.Close()（OracleIns.Close）统一关闭
	if cfg != nil {
		logs.Info(ctx, "Closing configuration resources...")
		cfg.Close()
		logs.Info(ctx, "Configuration resources closed")
	}

	logs.Info(ctx, "Flushing logs...")
	logs.Flush()
	logs.Close()
}

// gracefulShutdown 等待退出信号并在超时预算内完成全部清理。
// 返回的 channel 在清理（含日志落盘）完成后关闭；main() 必须等它，
// 否则 Go 运行时会在 main 返回时杀掉清理 goroutine，缓冲日志丢失。
func gracefulShutdown(fiberApp *fiber.App, cfg *config.Config, app *application) <-chan struct{} {
	allDone := make(chan struct{})

	go func() {
		defer close(allDone)

		ctx := context.Background()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

		sig := <-sigCh
		logs.Info(ctx, "Received shutdown signal: %v, starting graceful shutdown...", sig)

		shutdownCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownDuration())
		defer cancel()

		done := make(chan struct{}, 1)
		go func() {
			app.shutdownComponents(ctx, shutdownCtx, fiberApp, cfg)
			done <- struct{}{}
		}()

		select {
		case <-done:
			log.Println("✓ Graceful shutdown completed successfully")
		case <-shutdownCtx.Done():
			log.Printf("✗ Graceful shutdown timed out after %v — forcing exit\n", cfg.ShutdownDuration())
			logs.Flush()
		}
	}()

	return allDone
}

// displayServerInfos logs the startup banner.
func displayServerInfos(cfg *config.Config, fiberApp *fiber.App) {
	ctx := context.Background()
	logs.Info(ctx, "═══════════════════════════════════════════════════")
	logs.Info(ctx, "%s (fiber v%s)", cfg.Name, fiber.Version)
	logs.Info(ctx, "Server URL: http://127.0.0.1:%d", cfg.Port)
	logs.Info(ctx, "Bound on host %s and port %d", cfg.Host, cfg.Port)
	logs.Info(ctx, "Handlers: %d | PID: %d | GOMAXPROCS: %d",
		fiberApp.HandlersCount(), os.Getpid(), runtime.GOMAXPROCS(0))
	logs.Info(ctx, "═══════════════════════════════════════════════════")
}

// ── Entry point ───────────────────────────────────────────────────────────────

// @title			TCG-AI-ENGINE API
// @version		1.0
// @description	电商风控规则引擎服务（grule），支持规则热更新
// @BasePath		/
func main() {
	flag.Parse()

	var cfg config.Config
	cfg.Init(*configFile)

	app, err := newApplication(&cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}

	// ── pprof server (optional) ───────────────────────────────────
	if cfg.Pprof.Enabled && !fiber.IsChild() {
		addr := net.JoinHostPort(cfg.Pprof.Host, strconv.Itoa(cfg.Pprof.Port))
		logs.Info(context.Background(), "serving pprof on http://%s/debug/pprof", addr)

		srv := &http.Server{
			Addr:              addr,
			Handler:           nil, // DefaultServeMux，含 net/http/pprof 注册的路由
			ReadTimeout:       10 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    1 << 20,
		}
		app.pprofServer = srv

		gos.GoSafe(func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logs.Fatalf(context.Background(), "pprof server failed: %v", err)
			}
		})
	}

	// ── Fiber app ─────────────────────────────────────────────────
	fiberApp := fiber.New(fiber.Config{
		AppName:           cfg.Name,
		StrictRouting:     true,
		CaseSensitive:     true,
		Immutable:         true,
		ReduceMemoryUsage: true,
		BodyLimit:         cfg.BodyLimit,
		ReadTimeout:       time.Duration(cfg.Timeout) * time.Second,
		WriteTimeout:      time.Duration(cfg.Timeout) * time.Second,
		IdleTimeout:       120 * time.Second,
		ServerHeader:      cfg.Name,
		JSONEncoder:       sonic.Marshal,
		JSONDecoder:       sonic.Unmarshal,
		ErrorHandler:      middleware.ErrorHandler,
	})

	fiberApp.Use(cors.New())
	fiberApp.Use(recover.New(recover.Config{EnableStackTrace: true}))

	if cfg.Telemetry.Enabled {
		fiberApp.Use(middleware.EnableOtelTrace(middleware.OtelConfig{
			SkipPaths: cfg.Telemetry.SkipPaths,
		}))
	}

	fiberApp.Use(middleware.NewBehaviorLogger(cfg.Log.ServiceName).Handle())
	fiberApp.Use(middleware.AccessLog(app.zapLogger))

	// ── Routes ────────────────────────────────────────────────────
	router.RegisterPrometheus(fiberApp, &cfg)
	router.RegisterHandlers(fiberApp, app.riskHandler, app.rulesHandler, &cfg)
	router.Init(fiberApp, &cfg)

	// 兜底：main() 任何返回路径都把缓冲日志刷掉
	defer logs.Close()

	// ── Graceful shutdown ─────────────────────────────────────────
	shutdownDone := gracefulShutdown(fiberApp, &cfg, app)

	// ── Listen ────────────────────────────────────────────────────
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	displayServerInfos(&cfg, fiberApp)

	if err := fiberApp.Listen(addr, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
		logs.Fatalf(context.Background(), "Service failed to start: %v", err)
	}

	// 等清理 goroutine 完成全部收尾（停轮询、关连接、日志落盘）再让 main 返回
	<-shutdownDone
}
