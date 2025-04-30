package utils

import (
	"path/filepath"
	"strings"

	gitutils "github.com/ai-microsoft/haystack/utils/git"
)

type SimpleFilter struct {
	rootPath string
	negate   bool
	ignore   *gitutils.GitIgnoreRules
}

func (f *SimpleFilter) Match(path string, isDir bool) bool {
	if f.ignore == nil {
		return true
	}

	r := f.ignore.IsIgnored(filepath.Join(f.rootPath, path), isDir)
	if f.negate {
		return !r
	}

	return r
}

func NewSimpleFilterExclude(patterns []string, baseDir string) *SimpleFilter {
	if !filepath.IsAbs(baseDir) {
		return nil
	}

	ignore, _ := gitutils.NewGitIgnoreRulesFromString(strings.Join(patterns, "\n"), baseDir, true)
	return &SimpleFilter{
		rootPath: baseDir,
		negate:   true,
		ignore:   ignore,
	}
}

func NewSimpleFilter(patterns []string, baseDir string) *SimpleFilter {
	if !filepath.IsAbs(baseDir) {
		return nil
	}

	ignore, _ := gitutils.NewGitIgnoreRulesFromString(strings.Join(patterns, "\n"), baseDir, true)
	return &SimpleFilter{
		rootPath: baseDir,
		negate:   false,
		ignore:   ignore,
	}
}
