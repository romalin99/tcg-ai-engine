package logs

import (
	"log"
	"os"
	"path/filepath"

	"github.com/natefinch/lumberjack"
	"go.uber.org/zap/zapcore"
)

func getConsoleWriter() zapcore.WriteSyncer {
	return zapcore.AddSync(os.Stdout)
}

func getFileWriter(cfg Config) (zapcore.WriteSyncer, *lumberjack.Logger) {
	if !ensureLogDir(cfg.FileInfo.Path, "log") {
		return zapcore.AddSync(os.Stdout), nil
	}
	logFile := filepath.Join(cfg.FileInfo.Path, cfg.Name+".log")
	log.Printf("Log file path: %s", logFile)
	lumber := newLumberjack(logFile, cfg)
	writer := zapcore.NewMultiWriteSyncer(
		zapcore.AddSync(lumber),
		zapcore.AddSync(os.Stdout),
	)
	return writer, lumber
}

func defaultInt(val, d int) int {
	if val <= 0 {
		return d
	}
	return val
}

// newLumberjack creates a rotating file logger using Config values,
// falling back to sensible defaults for any zero values.
func newLumberjack(filename string, cfg Config) *lumberjack.Logger {
	return &lumberjack.Logger{
		Filename:   filename,
		MaxSize:    defaultInt(cfg.MaxSize, 500),
		MaxBackups: defaultInt(cfg.MaxBackups, 7),
		MaxAge:     defaultInt(cfg.KeepDays, 5),
		Compress:   cfg.Compress,
		LocalTime:  true,
	}
}

// GetLumberJackLogger returns the active lumberjack logger used for log rotation.
// Returns nil when logging to console or before the logger is initialised.
func GetLumberJackLogger() *lumberjack.Logger {
	mu.RLock()
	defer mu.RUnlock()
	if gLogger == nil {
		return nil
	}
	return gLogger.lumber
}

func ensureLogDir(dir, kind string) bool {
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		log.Printf("Failed to create %s directory %s: %v", kind, dir, err)
		return false
	}
	return true
}

// getBehaviorFileWriter creates a rotating file writer for behavior logs
// (API request logs: URI, status, elapsed). Path: PathBehavior/Name-behavior.log.
func getBehaviorFileWriter(cfg Config) (zapcore.WriteSyncer, *lumberjack.Logger) {
	if cfg.FileInfo.PathBehavior == "" || !ensureLogDir(cfg.FileInfo.PathBehavior, "behavior log") {
		return nil, nil
	}
	behaviorFile := filepath.Join(cfg.FileInfo.PathBehavior, cfg.Name+"-behavior.log")
	log.Printf("Log behavior file path: %s", behaviorFile)
	lumber := newLumberjack(behaviorFile, cfg)
	return zapcore.AddSync(lumber), lumber
}
