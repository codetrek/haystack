package server

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-microsoft/haystack/shared/running"
)

// StartServer initializes and starts the HTTP server
func StartServer(wg *sync.WaitGroup, addr string, socketPath string) {
	wg.Add(1)
	defer wg.Done()

	var shuttingDown atomic.Bool

	http.HandleFunc("/", http.NotFound)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/api/v1/server/restart", handleRestart)
	http.HandleFunc("/api/v1/server/stop", handleStop)
	http.HandleFunc("/api/v1/server/status", handleStatus)

	http.HandleFunc("/api/v1/document/update", handleUpdateDocument)
	http.HandleFunc("/api/v1/document/delete", handleDeleteDocument)

	http.HandleFunc("/api/v1/workspace/create", handleCreateWorkspace)
	http.HandleFunc("/api/v1/workspace/delete", handleDeleteWorkspace)
	http.HandleFunc("/api/v1/workspace/list", handleListWorkspace)
	http.HandleFunc("/api/v1/workspace/get", handleGetWorkspace)
	http.HandleFunc("/api/v1/workspace/sync-all", handleSyncAllWorkspaces)
	http.HandleFunc("/api/v1/workspace/sync", handleSyncWorkspace)
	http.HandleFunc("/api/v1/workspace/update", handleUpdateWorkspace)

	http.HandleFunc("/api/v1/search/content", handleSearchContent)
	http.HandleFunc("/api/v1/search/files", handleSearchFiles)
	http.HandleFunc("/api/v1/search/symbols", handleSearchSymbols)
	http.HandleFunc("/api/v1/search/prompts", handleSearchPrompts)

	if addr != "" {
		mcpInit(addr)
	} else {
		log.Println("[HTTP] No TCP address provided, skipping MCP server initialization")
	}

	// Start TCP server if address is provided
	var tcpServer *http.Server
	if addr != "" {
		tcpServer = &http.Server{
			Addr: addr,
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
		unixSocketServer = &http.Server{}
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
