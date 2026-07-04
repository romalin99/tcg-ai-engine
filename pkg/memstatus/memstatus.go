package memstatus

import (
	"context"
	"runtime"
	"time"

	"tcg-ai-engine/pkg/logs"
)

func bToMb(b uint64) float64 {
	return float64(b) / 1024 / 1024
}

// MemStats periodically logs Go runtime memory statistics until ctx is cancelled.
// Intended to be run as a goroutine: go memstatus.MemStats(ctx).
func MemStats(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			logs.Warn(ctx,
				"Alloc:%.2fMB,"+
					"TotalAlloc:%.2fMB,"+
					"Sys:%.2fMB,"+
					"HeapAlloc:%.2fMB,"+
					"HeapIdle:%.2fMB,"+
					"HeapInuse:%.2fMB,"+
					"HeapObjects:%v,"+
					"StackInuse:%.2fMB,"+
					"StackSys:%.2fMB,"+
					"OtherSys:%.2fMB,"+
					"NumGC:%d,"+
					"NumForcedGC:%v,"+
					"spanC:%.2fMB,"+
					"mcache:%.2fMB,"+
					"BuckHashSys:%.2fMB,"+
					"Goroutines:%d",
				bToMb(ms.Alloc),
				bToMb(ms.TotalAlloc),
				bToMb(ms.Sys),
				bToMb(ms.HeapAlloc),
				bToMb(ms.HeapIdle),
				bToMb(ms.HeapInuse),
				ms.HeapObjects,
				bToMb(ms.StackInuse),
				bToMb(ms.StackSys),
				bToMb(ms.OtherSys),
				ms.NumGC,
				ms.NumForcedGC,
				bToMb(ms.MSpanInuse),
				bToMb(ms.MCacheInuse),
				bToMb(ms.BuckHashSys),
				runtime.NumGoroutine())
		case <-ctx.Done():
			return
		}
	}
}
