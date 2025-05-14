# Changelog

## [next]
- Feature: Finding subparts of camelCase and snake_cased words
- Add exact phrase matching using quotes
- Add comprehensive test coverage for search functionality

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
