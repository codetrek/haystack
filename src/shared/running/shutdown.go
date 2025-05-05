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
	shutdown     context.Context
	cancel       func()
	shutdownOnce *sync.Once
	restart      atomic.Bool

	ErrShutdown = errors.New("server is shutting down")
)

func InitShutdown(wg *sync.WaitGroup) {
	restart.Store(false)

	shutdown, cancel = context.WithCancel(context.Background())
	wg.Add(1)
	shutdownOnce = &sync.Once{}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		defer wg.Done()

		select {
		case <-c:
			log.Println("[Running] Received interrupt signal, shutting down...")
			Shutdown()
		case <-shutdown.Done():
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
	shutdownOnce.Do(func() {
		cancel()
	})
}

func GetShutdown() context.Context {
	return shutdown
}

func WaitingForShutdown() {
	<-shutdown.Done()
}

func IsShuttingDown() bool {
	select {
	case <-shutdown.Done():
		return true
	default:
		return false
	}
}
