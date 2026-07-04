package gos

import (
	"sync"

	"github.com/alitto/pond/v2"
)

const defaultPoolSize = 1000

var (
	goPool pond.Pool
	once   sync.Once
)

func InitGoroutinePool(num int) {
	once.Do(func() {
		if num <= 0 {
			num = defaultPoolSize
		}
		goPool = pond.NewPool(num, pond.WithQueueSize(20000))
	})
}

func GetPool() pond.Pool {
	InitGoroutinePool(0)
	return goPool
}

func Resize(count int) {
	if goPool == nil {
		InitGoroutinePool(0)
	}
	goPool.Resize(count)
}

func ReleasePool() {
	if goPool != nil {
		goPool.StopAndWait()
		goPool = nil
	}
}
