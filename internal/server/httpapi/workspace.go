package httpapi

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"

	"github.com/codetrek/haystack/internal/core/workspace"
	"github.com/codetrek/haystack/internal/server/indexer"
	"github.com/codetrek/haystack/internal/shared/types"
)

func handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var request types.CreateWorkspaceRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Server] Recovered from panic: %v", r)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	ws, _ := workspace.GetByPath(request.Workspace)
	if ws != nil {
		log.Printf("[Server] Create workspace `%s`: already exists", request.Workspace)
		json.NewEncoder(w).Encode(types.CreateWorkspaceResponse{
			Code:    0,
			Message: "Workspace already exists",
			Data: types.Workspace{
				Id:           ws.Id,
				Path:         ws.Path,
				CreatedAt:    ws.CreatedAt,
				LastAccessed: ws.LastAccessed,
				LastFullSync: ws.GetLastFullSync(),
				Indexing:     true,
			},
		})
		return
	}

	ws, err = indexer.CreateWorkspace(request.Workspace, request.UseGlobalFilters, request.Filters)
	if err != nil {
		log.Printf("[Server] Create workspace `%s`: failed to get or create: %v", request.Workspace, err)
		json.NewEncoder(w).Encode(types.CommonResponse{
			Code:    1,
			Message: fmt.Sprintf("Failed to create workspace: %v", err),
		})
		return
	}

	log.Printf("[Server] Created workspace `%s`", request.Workspace)
	json.NewEncoder(w).Encode(types.CreateWorkspaceResponse{
		Code:    0,
		Message: "Ok",
		Data: types.Workspace{
			Id:           ws.Id,
			Path:         ws.Path,
			CreatedAt:    ws.CreatedAt,
			LastAccessed: ws.LastAccessed,
			LastFullSync: ws.GetLastFullSync(),
			Indexing:     true,
		},
	})
}

func handleUpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	var request types.UpdateWorkspaceRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Server] Recovered from panic: %v", r)
			return
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	ws, err := workspace.GetByPath(request.Workspace)
	if err != nil {
		log.Printf("[Server] Update workspace `%s`: failed to get: %v", request.Workspace, err)
		json.NewEncoder(w).Encode(types.CommonResponse{
			Code:    1,
			Message: fmt.Sprintf("Failed to update workspace: %v", err),
		})
		return
	}

	ws.UseGlobalFilters = request.UseGlobalFilters
	ws.Filters = request.Filters

	err = ws.Save()
	if err != nil {
		log.Printf("[Server] Update workspace `%s`: failed to save: %v", request.Workspace, err)
		json.NewEncoder(w).Encode(types.CommonResponse{
			Code:    1,
			Message: fmt.Sprintf("Failed to update workspace: %v", err),
		})
		return
	}

	log.Printf("[Server] Updated workspace `%s`", request.Workspace)
	json.NewEncoder(w).Encode(types.UpdateWorkspaceResponse{
		Code:    0,
		Message: "Ok",
		Data: types.Workspace{
			Id: ws.Id,
		},
	})
}

func handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	var request types.DeleteWorkspaceRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if request.Workspace == "" {
		json.NewEncoder(w).Encode(types.CommonResponse{
			Code:    1,
			Message: "Workspace path is required",
		})
		return
	}

	if !filepath.IsAbs(request.Workspace) {
		json.NewEncoder(w).Encode(types.CommonResponse{
			Code:    2,
			Message: "Workspace path must be absolute",
		})
		return
	}

	ws, err := workspace.GetByPath(request.Workspace)
	if err != nil {
		log.Printf("[Server] Delete workspace `%s`: failed to get: %v", request.Workspace, err)
		json.NewEncoder(w).Encode(types.CommonResponse{
			Code:    3,
			Message: fmt.Sprintf("Failed to delete workspace: %v", err),
		})
		return
	}

	err = workspace.Delete(ws.Id)
	if err != nil {
		log.Printf("[Server] Delete workspace `%s`: failed to delete: %v", request.Workspace, err)
		json.NewEncoder(w).Encode(types.CommonResponse{
			Code:    1,
			Message: fmt.Sprintf("Failed to delete workspace: %v", err),
		})
		return
	}

	log.Printf("[Server] Deleted workspace `%s`", request.Workspace)
	json.NewEncoder(w).Encode(types.DeleteWorkspaceResponse{
		Code:    0,
		Message: "Deleted",
		Data: types.Workspace{
			Id:           ws.Id,
			Path:         ws.Path,
			TotalFiles:   ws.GetTotalFiles(),
			CreatedAt:    ws.CreatedAt,
			LastAccessed: ws.LastAccessed,
			LastFullSync: ws.GetLastFullSync(),
			Indexing:     false,
		},
	})
}

func handleListWorkspace(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Server] Recovered from panic: %v", r)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	json.NewEncoder(w).Encode(types.ListWorkspaceResponse{
		Code:    0,
		Message: "Ok",
		Data: types.Workspaces{
			Workspaces: workspace.GetAll(),
		},
	})
}

func handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	var request types.GetWorkspaceRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ws, err := workspace.GetByPath(request.Workspace)
	if err != nil {
		log.Printf("[Server] Get workspace `%s`: %v", request.Workspace, err)
		json.NewEncoder(w).Encode(types.GetWorkspaceResponse{
			Code:    1,
			Message: "Not found",
		})
		return
	}

	totalFiles := ws.GetTotalFiles()
	indexing := ws.GetIndexingStatus() != nil

	json.NewEncoder(w).Encode(types.GetWorkspaceResponse{
		Code:    0,
		Message: "Ok",
		Data: &types.Workspace{
			Id:               ws.Id,
			Path:             ws.Path,
			TotalFiles:       totalFiles,
			UseGlobalFilters: ws.UseGlobalFilters,
			Filters:          ws.Filters,
			CreatedAt:        ws.CreatedAt,
			LastAccessed:     ws.LastAccessed,
			LastFullSync:     ws.LastFullSync,
			Indexing:         indexing,
		},
	})
}

func handleSyncAllWorkspaces(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	workspaces := workspace.GetAllPaths()
	for _, workspacePath := range workspaces {
		if ws, err := workspace.GetByPath(workspacePath); err == nil {
			indexer.Sync(ws, false)
		}
	}

	json.NewEncoder(w).Encode(types.CommonResponse{
		Code:    0,
		Message: "Sync all in progress...",
	})
}

func handleSyncWorkspace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	var request types.SyncWorkspaceRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ws, err := workspace.GetByPath(request.Workspace)
	if err != nil {
		log.Printf("[Server] Sync workspace `%s`: %v", request.Workspace, err)
		json.NewEncoder(w).Encode(types.CommonResponse{
			Code:    1,
			Message: fmt.Sprintf("Failed to get workspace: %v", err),
		})
		return
	}

	log.Printf("[Server] Requesting sync for workspace `%s`", ws.Path)

	indexer.Sync(ws, false)

	json.NewEncoder(w).Encode(types.CommonResponse{
		Code:    0,
		Message: "Sync in progress...",
	})
}

func handleMoveWorkspace(w http.ResponseWriter, r *http.Request) {
	var request types.MoveWorkspaceRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		log.Printf("[Server] Move workspace: failed to decode request: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Server] Recovered from panic: %v", r)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	log.Printf("[Server] Move workspace request: %v", request)
	ws, err := workspace.Move(request.Id, request.NewPath)
	if err != nil {
		log.Printf("[Server] Move workspace `%v`: failed to save: %v", request.NewPath, err)
		json.NewEncoder(w).Encode(types.CommonResponse{
			Code:    1,
			Message: fmt.Sprintf("Failed to update workspace: %v", err),
		})
		return
	}

	json.NewEncoder(w).Encode(types.MoveWorkspaceResponse{
		Code:    0,
		Message: "Ok",
		Data: types.Workspace{
			Id:           ws.Id,
			Path:         ws.Path,
			TotalFiles:   ws.GetTotalFiles(),
			CreatedAt:    ws.CreatedAt,
			LastAccessed: ws.LastAccessed,
			LastFullSync: ws.GetLastFullSync(),
			Indexing:     false,
		},
	})
}
