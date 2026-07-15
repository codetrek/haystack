package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/codetrek/haystack/server/internal/conf"
	"github.com/codetrek/haystack/server/internal/shared/running"
	"github.com/codetrek/haystack/server/internal/shared/types"
)

// handleHealth handles the health check endpoint
func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	response := types.HealthResponse{
		Code:    0,
		Message: "healthy",
		Data: types.HealthInfo{
			DataPath: conf.Get().Global.DataPath,
			PID:      os.Getpid(),
			Version:  running.Version(),
		},
	}

	json.NewEncoder(w).Encode(response)
}

// handleRestart handles the restart endpoint
// It will restart the server by calling the restart function
func handleRestart(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Server] Recovered from panic: %v", r)
			return
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	type RestartResponse struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}

	response := RestartResponse{
		Code:    0,
		Message: "restarting",
	}

	json.NewEncoder(w).Encode(response)

	log.Println("[Server] Server restart requested")
	running.Restart()
}

// handleStop handles the stop endpoint
// It will stop the server by calling the shutdown function
func handleStop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	type StopResponse struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}

	response := StopResponse{
		Code:    0,
		Message: "stopping",
	}

	json.NewEncoder(w).Encode(response)

	log.Println("[Server] Stop requested")
	running.Shutdown()
}

// handleStatus handles the status endpoint
// It will return the status of the server
func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	type StatusResponse struct {
		Code    int                `json:"code"`
		Message string             `json:"message"`
		Data    types.ServerStatus `json:"data"`
	}

	response := StatusResponse{
		Code:    0,
		Message: "Ok",
		Data: types.ServerStatus{
			ShuttingDown: running.IsShuttingDown(),
			Restarting:   running.IsRestart(),
			PID:          os.Getpid(),
			Version:      running.Version(),
			DataPath:     conf.Get().Global.DataPath,
		},
	}

	json.NewEncoder(w).Encode(response)
}
