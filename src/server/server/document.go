package server

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/server/indexer"
	"github.com/ai-microsoft/haystack/shared/types"
)

func handleUpdateDocument(w http.ResponseWriter, r *http.Request) {
	var request types.DocumentUpdateRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	workspace, err := workspace.GetByPath(request.Workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = indexer.AddOrSyncFile(workspace, request.Path)
	if err != nil {
		log.Printf("Failed to update `%s` in workspace `%s`: %v", request.Path, workspace.Path, err)

		json.NewEncoder(w).Encode(types.CommonResponse{
			Code:    1,
			Message: err.Error(),
		})
		return
	}

	log.Printf("Updated `%s` in workspace `%s`", request.Path, workspace.Path)
	json.NewEncoder(w).Encode(types.CommonResponse{
		Code:    0,
		Message: "Ok",
	})
}

func handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	var request types.DocumentDeleteRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	workspace, err := workspace.GetByPath(request.Workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	indexer.RemoveFile(workspace, request.Path)
	log.Printf("Deleted `%s` in workspace `%s`", request.Path, workspace.Path)

	json.NewEncoder(w).Encode(types.CommonResponse{
		Code:    0,
		Message: "Ok",
	})
}
