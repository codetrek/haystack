package types

type DocumentUpdateRequest struct {
	Workspace string `json:"workspace"`
	Path      string `json:"path"` // relative path to the workspace
}

type DocumentDeleteRequest struct {
	Workspace string `json:"workspace"`
	Path      string `json:"path"` // relative path to the workspace
}
