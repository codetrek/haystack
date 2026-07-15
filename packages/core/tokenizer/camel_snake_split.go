package tokenizer

import (
	"strings"
	"unicode"
)

// 1. handleUpdateDocument => HandleUpdateDocument, UpdateDocument, Document
// 2. handle-update-document => handle-update-document, update-document, document
// 3. handle_update_document => handle_update_document, update_document, document
// 4. beginHandle_Update-Document => beginHandle_Update-Document, Handle_Update-Document, Update-Document, Document
// 5. beginHANDLEUpdate_document => beginHANDLEUpdate_document, HANDLEUpdate_document, Update_document, document
// 6. BEGINHandleUpdate_document => BEGINHandleUpdate_document, HandleUpdate_document, Update_document, document
func CamelSnakeSplit(s string) []string {
	r := camelSnakeSplitInto(nil, s)
	if r == nil {
		return []string{}
	}
	return r
}

// camelSnakeSplitInto appends the camel/snake sub-tokens of s to dst and returns
// the extended slice (dst is returned unchanged when s is empty). The sub-tokens
// are slices of s (no copy), so this lets hot callers reuse one buffer across many
// tokens instead of allocating a fresh result slice per token. CamelSnakeSplit is
// the allocating convenience wrapper.
func camelSnakeSplitInto(dst []string, s string) []string {
	s = strings.Trim(s, ".-_")
	if len(s) == 0 {
		return dst
	}

	var last = rune(s[0])
	for i, r := range s {
		if i == 0 {
			dst = append(dst, s)
			continue
		}

		sub := ""
		if (last == '-' || last == '_' || last == '.') && (r != '-' && r != '_' && r != '.') {
			sub = s[i:]
		} else if unicode.IsUpper(r) && !unicode.IsUpper(last) {
			sub = s[i:]
		} else if unicode.IsUpper(r) && unicode.IsUpper(last) {
			if i+1 < len(s) && unicode.IsLower(rune(s[i+1])) {
				sub = s[i:]
			}
		}

		last = r
		if len(sub) == 0 {
			continue
		}
		if len(sub) < 3 {
			break
		}
		dst = append(dst, sub)
	}

	return dst
}
