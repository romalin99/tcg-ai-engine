package gos

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"

	"tcg-ai-engine/pkg/logs"
)

// GoSafe starts fn in a new goroutine wrapped with Recover.
// If fn panics, the panic is logged via the structured logger, the log
// buffer is flushed, and the goroutine exits gracefully (the process
// continues).
func GoSafe(fn func()) {
	go RunSafe(fn)
}

// RunSafe executes fn in the current goroutine with panic recovery.
// Useful when you already have a goroutine (e.g. via sync.WaitGroup.Go)
// and just want the recovery wrapper.
func RunSafe(fn func()) {
	defer Recover()
	fn()
}

// Recover should be called via defer inside a goroutine to catch panics,
// log them through the structured log system, and flush the log buffer so
// no entries are lost.
//
// Usage:
//
//	go func() {
//	    defer gos.Recover()
//	    // ... work ...
//	}()
func Recover() {
	if p := recover(); p != nil {
		stack := debug.Stack()
		logs.Err(context.Background(),
			"goroutine recovered from panic: %v\n%s", p, stack)
		logs.Flush()

		// Also write to stderr as a last resort in case the logger itself
		// is broken or its buffer can't be flushed.
		fmt.Fprintf(os.Stderr,
			"[PANIC-RECOVER] goroutine panic: %+v\n%s\n", p, stack,
		)
	}
}

// RecoverThenCrash is like Recover but re-panics after flushing logs.
// Use this for goroutines where a panic means the process MUST crash, but
// you still want the buffered log entries written out first.
//
// Usage:
//
//	go func() {
//	    defer gos.RecoverThenCrash()
//	    // ... critical work ...
//	}()
func RecoverThenCrash() {
	if p := recover(); p != nil {
		stack := debug.Stack()
		logs.Err(context.Background(),
			"goroutine fatal panic (will re-panic): %v\n%s", p, stack)
		logs.Flush()

		fmt.Fprintf(os.Stderr,
			"[FATAL-PANIC] goroutine panic: %+v\n%s\n", p, stack,
		)
		panic(p) // re-panic after flushing
	}
}
