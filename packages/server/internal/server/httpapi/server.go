package httpapi

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codetrek/haystack/server/internal/shared/running"
)

// StartServer initializes and starts the HTTP server
func StartServer(wg *sync.WaitGroup, addr string, socketPath string) {
	wg.Add(1)
	defer wg.Done()

	var shuttingDown atomic.Bool

	mux := http.NewServeMux()

	mux.HandleFunc("/", http.NotFound)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/api/v1/server/restart", handleRestart)
	mux.HandleFunc("/api/v1/server/stop", handleStop)
	mux.HandleFunc("/api/v1/server/status", handleStatus)

	mux.HandleFunc("/api/v1/document/update", handleUpdateDocument)
	mux.HandleFunc("/api/v1/document/delete", handleDeleteDocument)

	mux.HandleFunc("/api/v1/workspace/create", handleCreateWorkspace)
	mux.HandleFunc("/api/v1/workspace/delete", handleDeleteWorkspace)
	mux.HandleFunc("/api/v1/workspace/list", handleListWorkspace)
	mux.HandleFunc("/api/v1/workspace/get", handleGetWorkspace)
	mux.HandleFunc("/api/v1/workspace/sync-all", handleSyncAllWorkspaces)
	mux.HandleFunc("/api/v1/workspace/sync", handleSyncWorkspace)
	mux.HandleFunc("/api/v1/workspace/update", handleUpdateWorkspace)
	mux.HandleFunc("/api/v1/workspace/move", handleMoveWorkspace)

	mux.HandleFunc("/api/v1/search/content", handleSearchContent)
	mux.HandleFunc("/api/v1/search/files", handleSearchFiles)
	mux.HandleFunc("/api/v1/search/symbols", handleSearchSymbols)

	if addr != "" {
		mcpInit(addr, mux)
	} else {
		log.Println("[HTTP] No TCP address provided, skipping MCP server initialization")
	}

	// Start TCP server if address is provided
	var tcpServer *http.Server
	if addr != "" {
		tcpServer = &http.Server{
			Addr:    addr,
			Handler: mux,
		}
		go func() {
			log.Printf("[HTTP] Server starting on %s", addr)
			if err := tcpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatal("[HTTP] Error: ListenAndServe failed: ", err)
			}
		}()
	} else {
		log.Println("[HTTP] Skipping TCP server")
	}

	// Start unix socket server if socket path is provided
	var unixSocketServer *http.Server
	if socketPath != "" {
		if _, err := os.Stat(socketPath); err == nil {
			if errRemove := os.Remove(socketPath); errRemove != nil {
				log.Fatalf("[HTTP] Failed to remove existing socket file %s: %v", socketPath, errRemove)
			}
		}
		unixSocketListener, err := net.Listen("unix", socketPath)
		if err != nil {
			log.Fatalf("[HTTP] Error: Listen on unix socket %s failed: %v", socketPath, err)
		}
		unixSocketServer = &http.Server{Handler: mux}
		go func() {
			log.Printf("[HTTP] Unix socket server starting on %s", socketPath)
			err := unixSocketServer.Serve(unixSocketListener)

			// Remove the socket file now
			os.Remove(socketPath)

			if err != nil && err != http.ErrServerClosed {
				log.Fatal("[HTTP] Error: Serve on unix socket failed: ", err)
			}
		}()
	} else {
		log.Println("[HTTP] Skipping unix socket server")
	}

	// Wait for shutdown signal
	<-running.GetShutdown().Done()
	shuttingDown.Store(true)

	// Create shutdown context with 5 second timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Wait for all two servers to shutdown gracefully
	var shutdownWg sync.WaitGroup
	shutdownWg.Add(2)

	log.Println("[HTTP] Stopping server...")

	// Shutdown TCP server
	go func() {
		defer shutdownWg.Done()
		if tcpServer != nil {
			if err := tcpServer.Shutdown(ctx); err != nil {
				log.Printf("[HTTP] TCP server forced to shutdown: %v", err)
			}
		}
	}()
	// Shutdown unix socket serve
	go func() {
		defer shutdownWg.Done()
		if unixSocketServer != nil {
			if err := unixSocketServer.Shutdown(ctx); err != nil {
				log.Printf("[HTTP] Unix socket server forced to shutdown: %v", err)
			}
		}
	}()

	shutdownWg.Wait()
	log.Println("[HTTP] Server stopped")
}
