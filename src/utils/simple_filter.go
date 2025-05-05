package utils

import (
	"path/filepath"

	gitignore "github.com/sabhiram/go-gitignore"
)

type SimpleFilter struct {
	negate bool
	//	ignore   *gitutils.GitIgnoreRules
	ignore   *gitignore.GitIgnore
	patterns []string
}

// Match checks if the given relative path matches the filter's patterns.
// The relPath parameter must be a relative path, and it will be converted
// to a slash-separated format internally. If isDir is true, a trailing slash
// will be appended to relPath before matching.
func (f *SimpleFilter) Match(relPath string, isDir bool) bool {
	if f.ignore == nil {
		return true
	}

	relPath = "/" + filepath.ToSlash(relPath)
	if isDir {
		relPath += "/"
	}

	match := f.ignore.MatchesPath(relPath)
	if f.negate {
		return !match
	}

	return match
}

func NewSimpleFilterExclude(patterns []string) *SimpleFilter {
	ignore := gitignore.CompileIgnoreLines(patterns...)
	return &SimpleFilter{
		negate:   true,
		ignore:   ignore,
		patterns: patterns,
	}
}

func NewSimpleFilter(patterns []string) *SimpleFilter {
	ignore := gitignore.CompileIgnoreLines(patterns...)
	return &SimpleFilter{
		negate:   false,
		ignore:   ignore,
		patterns: patterns,
	}
}
