# Search Query Syntax Guide [WIP]

## Overview

This document describes the search query syntax used in our search system. The search engine supports various logical operators, wildcards, and grouping to help you find exactly what you're looking for.

## Logical Operators

| Operator | Symbol | Description | Usage Example |
|----------|--------|-------------|---------------|
| AND      | `AND` or space | Matches documents containing all specified terms | `cat AND dog` or `cat dog` |
| OR       | `OR` or `\|` | Matches documents containing any of the specified terms | `cat OR dog` or `cat \| dog` |

## Search Term Formats

### 1. Basic Terms
- **Single Word**: `hello`
- **Phrase Search**: `"hello world"` (use double quotes for exact phrase matching)
- **Case Sensitivity**: Search is case-insensitive by default

### 2. Wildcard Matching
- **Prefix Matching**: `hello*` (matches "hello", "hello2", "helloworld")
- **Multiple Wildcards**: `hel*o` (matches "hello", "helio", "hell ok")
  - **Note**: Wildcards can only be used at the end of a word or between characters, and at least 2 characters in prefix
    - `*ello` - invalid!
    - `h*ll` - invalid!
    - `he*l` - valid
    - `he*` - valid

### 3. Special Characters
- **Quotes**: `"exact phrase"` for exact matching

## Examples

### 1. Single Word Search
- `hello` → matches: "hello", "Hello", "HELLO", "hello world"
- `"hello world"` → matches: "hello world" (exact phrase only)

### 2. AND Search
- `cat dog` → matches line containing both "cat" and "dog"
- `cat* AND dog*` → matches: "cat dog", "cats dogs", "catalog doggy"
- `"cat food" AND "dog food"` → matches line containing both exact phrases

### 3. OR Search
- `cat OR dog` → matches line containing either "cat" or "dog"
- `cat* OR dog*` → matches: "cat", "cats", "dog", "dogs"

### 4. NOT Search
- `cat NOT dog` → matches line with "cat" but not "dog"
- `cat AND NOT dog` → same as above

## Performance Tips

1. **Use Specific Terms**
   - Prefer specific terms over wildcards when possible
   - Example: Use `cat` instead of `ca*` when you know the exact term

2. **Optimize NOT Queries**
   - Always combine NOT with AND to avoid performance issues
   - Bad: `NOT cat`
   - Good: `dog AND NOT cat`

3. **Phrase Search**
   - Use phrase search for exact matches
   - Example: `"cat food"` instead of `cat AND food`

## Common Use Cases

### 1. Exact Phrase Matching
```
"cat food" AND "dog food"
```

### 2. Wildcard Search
```
cat* AND food*
```

## Troubleshooting

1. **No Results Found**
   - Check for typos
   - Try removing wildcards
   - Use simpler terms

2. **Too Many Results**
   - Add more specific terms
   - Use phrase search
   - Add NOT conditions

3. **Performance Issues**
   - Avoid single NOT queries
   - Limit wildcard usage
   - Use more specific terms
