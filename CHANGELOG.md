# Changelog

## [1.7.0]
- feat: implement whole word matching feature

## [1.6.0]
- feat: implement unsaved-files-only search optimization for better search performance
- fix: Make file include/exclude patterns case-insensitive
- Support streamable-http at endpoint `http://localhost:<port>/mcp`

## [1.5.2]
- Search results now include changes from unsaved files.
- Increase max indexable file size to 5MiB.
- Bugfix: There will be no result if a file is passed in 'path' filter

## [1.5.1]
- Breaking change: Upgrade storage version to 1.3
- Bugfix: Path filter with "\" seperate returns no result.
- Optimize: Output search result in a single text block to improve GHC performance
- Feat: Support Unix domain socket for Non-Windows platform
- Feat: CTags based function parse and search

## [1.4.1]
- Optimize wildcard search(e.g.: `microsoft*bookmark`), up to 5X+ speed up for cases.
- Bugfix: active & open files should be search first
- Optimize: dedup keywords with prefix

## [1.4.0]
- Breaking change: Upgrade storage version to 1.2
- Make inverted index engine as a separated components
- Make "debug/", "release/" as default exclude pattern while scaning
- Speed up 10X+ for: `bool function`, `obj->func()`
- Supports finding substrings within CamelCase identifiers (e.g., `bookmarkbar` in `UpdateBookmarkBarIfNecessary`).
- Support leading special characters (e.g., `->AddBookmark`).

## [1.3.0]
- Feature: Finding subparts of camelCase and snake_cased words
- Add exact phrase matching using quotes
- Add comprehensive test coverage for search functionality
- Un-hex docids to reduce disk usage by 40%

## [1.2.6]
- Optimize document sorting
- Increase DB cache

## [1.2.5]
- Bugfix: Wrong position of HaystackSearch file seperation line
- Response search result in streaming for better experience
- Improve gitignore filter performance

## [1.2.3]
- Bugfix: MCP SSE connection lost after a while

## [1.2.2]
- Text file auto detection
- Add builtin exclude ext list
- Add Line before and after in search result
- Add more unittests

## [1.2.1]
- Merge inverted index
- Add APIs to change workspace settings
- Bugfix: Inverted indexes maybe dup-ed while sync progress
- Bugfix: File which exceeded the size limit shouldn't be read into memory
- Bugfix: OOM issue while re-sync folders

## [1.2.0]
- Add file search
- Add MCP Tool: HaystackFiles

## [1.1.0]
- Add MCP Tool: HaystackSearch
- Fixed serval critical bug

## [1.0.0]
- Inital version of haystack search
