package mcptools

import (
	"fmt"
	"path/filepath"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/utils"
)

func parseAndValidateSearchArgs(arguments map[string]any) (string, string, int, error) {
	query, ok1 := arguments["query"].(string)
	workspacePath, ok2 := arguments["workspace"].(string)
	limitCount, ok3 := arguments["limit"].(float64)

	if !ok1 || !ok2 {
		return "", "", 0, fmt.Errorf("invalid arguments")
	}

	workspacePath = utils.NormalizePath(workspacePath)
	if !filepath.IsAbs(workspacePath) {
		return "", "", 0, fmt.Errorf("workspace is not absolute")
	}

	limit := conf.Get().Client.DefaultLimit.MaxFilesResults
	if ok3 {
		limit = int(limitCount)
	}

	if limit > conf.Get().Server.Search.Limit.MaxFilesResults {
		limit = conf.Get().Server.Search.Limit.MaxFilesResults
	}

	return query, workspacePath, limit, nil
}
