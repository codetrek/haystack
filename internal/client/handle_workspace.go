package client

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/codetrek/haystack/internal/shared/running"
	"github.com/codetrek/haystack/internal/shared/types"
)

func handleWorkspace(args []string) {
	if wantsHelp(args) {
		fmt.Println("Usage: " + running.ExecutableName() + " workspace <command>")
		fmt.Println("Commands:")
		fmt.Println("  list                  List workspaces")
		fmt.Println("  get <path>            Get a workspace")
		fmt.Println("  create <path>         Create a new workspace")
		fmt.Println("  delete <path>         Delete a workspace")
		fmt.Println("  sync-all              Sync all workspaces")
		fmt.Println("  sync <path>           Sync a workspace")
		fmt.Println("  move <old> <new>      Move a workspace to new path")
		return
	}

	command := args[0]
	switch command {
	case "list":
		handleWorkspaceList()
	case "create":
		handleWorkspaceCreate(args[1])
	case "delete":
		handleWorkspaceDelete(args[1])
	case "sync-all":
		handleWorkspaceSyncAll()
	case "sync":
		handleWorkspaceSync(args[1])
	case "get":
		handleWorkspaceGet(args[1])
	case "move":
		handleWorkspaceMove(args[1], args[2])
	default:
		fmt.Printf("Unknown workspace command: %s\n", command)
		fmt.Println("Available commands: get, list, create, delete, sync, sync-all, move")
	}
}

func handleWorkspaceSyncAll() {
	result, err := serverRequest("/workspace/sync-all", []byte{})
	if err != nil {
		fmt.Printf("Error syncing all workspaces: %v\n", err)
		return
	}

	fmt.Println("Message:", result.Body.Message)
}

func handleWorkspaceSync(workspacePath string) {
	if workspacePath == "" {
		fmt.Println("Usage: " + running.ExecutableName() + " workspace sync <workspace path>")
		return
	}
	if !filepath.IsAbs(workspacePath) {
		fmt.Println("Workspace path must be absolute")
		return
	}

	request := types.SyncWorkspaceRequest{
		Workspace: workspacePath,
	}

	requestJson, err := json.Marshal(request)
	if err != nil {
		fmt.Printf("Error syncing workspace: %v\n", err)
		return
	}

	result, err := serverRequest("/workspace/sync", requestJson)
	if err != nil {
		fmt.Printf("Error syncing workspace: %v\n", err)
		return
	}

	fmt.Println("Message:", result.Body.Message)
}

func handleWorkspaceList() {
	result, err := serverRequest("/workspace/list", []byte{})
	if err != nil {
		fmt.Printf("Error listing workspaces: %v\n", err)
		return
	}

	var workspaces types.Workspaces
	if err := json.Unmarshal(*result.Body.Data, &workspaces); err != nil {
		fmt.Printf("Error listing workspaces: %v\n", err)
		return
	}

	for _, workspace := range workspaces.Workspaces {
		printWorkspace("", workspace)
	}
}

func handleWorkspaceGet(workspacePath string) {
	request := types.GetWorkspaceRequest{
		Workspace: workspacePath,
	}
	requestJson, err := json.Marshal(request)
	if err != nil {
		fmt.Printf("Error getting workspace: %v\n", err)
		return
	}

	result, err := serverRequest("/workspace/get", requestJson)
	if err != nil {
		fmt.Printf("Error getting workspace: %v\n", err)
		return
	}

	var workspace types.Workspace
	if err := json.Unmarshal(*result.Body.Data, &workspace); err != nil {
		fmt.Printf("Error getting workspace: %v\n", err)
		return
	}

	printWorkspace("", workspace)
}

func printWorkspace(prefix string, ws types.Workspace) {
	fmt.Printf(`%s %d:
  Path: %s
  Created at: %s
  Last accessed: %s
  Last full sync: %s
  Use global filters: %t
  Filters: %v
  Indexing: %t
`,
		prefix, ws.Id, ws.Path, ws.CreatedAt, ws.LastAccessed, ws.LastFullSync,
		ws.UseGlobalFilters, ws.Filters, ws.Indexing)
}

func handleWorkspaceCreate(workspacePath string) {
	if workspacePath == "" {
		fmt.Println("Usage: " + running.ExecutableName() + " workspace create <workspace path>")
		return
	}
	if !filepath.IsAbs(workspacePath) {
		fmt.Println("Workspace path must be absolute")
		return
	}

	request := types.CreateWorkspaceRequest{
		Workspace: workspacePath,
	}

	requestJson, err := json.Marshal(request)
	if err != nil {
		fmt.Printf("Error creating workspace: %v\n", err)
		return
	}

	result, err := serverRequest("/workspace/create", requestJson)
	if err != nil {
		fmt.Printf("Error creating workspace: %v\n", err)
		return
	}

	var response types.Workspace
	if err := json.Unmarshal(*result.Body.Data, &response); err != nil {
		fmt.Printf("Error creating workspace: %v\n", err)
		return
	}

	printWorkspace("Created workspace", response)
}

func handleWorkspaceDelete(workspacePath string) {
	if workspacePath == "" {
		fmt.Println("Usage: " + running.ExecutableName() + " workspace delete <workspace path>")
		return
	}

	request := types.DeleteWorkspaceRequest{
		Workspace: workspacePath,
	}
	requestJson, err := json.Marshal(request)
	if err != nil {
		fmt.Printf("Error deleting workspace: %v\n", err)
		return
	}

	result, err := serverRequest("/workspace/delete", requestJson)
	if err != nil {
		fmt.Printf("Error deleting workspace: %v\n", err)
		return
	}

	var response types.Workspace
	if err := json.Unmarshal(*result.Body.Data, &response); err != nil {
		fmt.Printf("Error deleting workspace: %v\n", err)
		return
	}

	printWorkspace("Deleted", response)
}

func handleWorkspaceMove(idStr string, newWorkspacePath string) {
	if idStr == "" || newWorkspacePath == "" {
		fmt.Println("Usage: " + running.ExecutableName() + " workspace move <workspace id> <new workspace path>")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		fmt.Printf("Invalid workspace ID: %v\n", err)
		return
	}

	request := types.MoveWorkspaceRequest{
		Id:      id,
		NewPath: newWorkspacePath,
	}
	requestJson, err := json.Marshal(request)
	if err != nil {
		fmt.Printf("Error marshaling JSON:  %v\n", err)
		return
	}

	result, err := serverRequest("/workspace/move", requestJson)
	if err != nil {
		fmt.Printf("Error encountered with server request: %v\n", err)
		return
	}

	var workspace types.Workspace
	if err := json.Unmarshal(*result.Body.Data, &workspace); err != nil {
		fmt.Printf("Error unmarshaling server response: %v\n", err)
		return
	}

	printWorkspace("Moved", workspace)
}
