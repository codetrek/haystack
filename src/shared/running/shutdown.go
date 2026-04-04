package running

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
)

var (
	mu           sync.Mutex
	shutdown     context.Context
	cancel       func()
	shutdownOnce *sync.Once
	restart      atomic.Bool

	ErrShutdown = errors.New("server is shutting down")
)

func InitShutdown(wg *sync.WaitGroup) {
	restart.Store(false)

	mu.Lock()
	shutdown, cancel = context.WithCancel(context.Background())
	shutdownOnce = &sync.Once{}
	// Capture local copies so the goroutine doesn't race on package-level vars.
	localShutdown := shutdown
	localOnce := shutdownOnce
	localCancel := cancel
	mu.Unlock()

	wg.Add(1)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		defer wg.Done()

		select {
		case <-c:
			log.Println("[Running] Received interrupt signal, shutting down...")
			localOnce.Do(func() {
				localCancel()
			})
		case <-localShutdown.Done():
		}
	}()
}

func Restart() {
	restart.Store(true)
	Shutdown()
}

func IsRestart() bool {
	return restart.Load()
}

func Shutdown() {
	mu.Lock()
	once := shutdownOnce
	fn := cancel
	mu.Unlock()

	once.Do(func() {
		fn()
	})
}

func GetShutdown() context.Context {
	mu.Lock()
	defer mu.Unlock()
	return shutdown
}

func WaitingForShutdown() {
	mu.Lock()
	ctx := shutdown
	mu.Unlock()
	<-ctx.Done()
}

func IsShuttingDown() bool {
	mu.Lock()
	ctx := shutdown
	mu.Unlock()

	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
