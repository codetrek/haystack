# tokenizer

Text tokenization for the `searchcore` inverted index. Package `tokenizer`
turns raw text (source code, comments, prose) into the normalized token sets
used both when indexing documents and when parsing search queries. It handles
ASCII identifiers (camelCase / snake_case / kebab-case), CJK (Chinese,
Japanese, Korean) segmentation, and stopword filtering.

## Responsibility

- Produce, for indexing, a sorted, de-duplicated set of normalized tokens from a
  string.
- Produce, for search, the query tokens plus any wildcard fragments extracted
  from the input.
- Route mixed-script text to the right per-script tokenizer and merge the
  results.

## Key interface

```go
type Tokenizer interface {
    // For indexing: returns sorted, unique, normalized tokens.
    TokenizeForIndex(str string) []string
    // For search: returns search tokens and any wildcard tokens.
    // When exactMatching is true, tokens are kept as-is, no wildcard handling.
    TokenizeForSearch(s string, exactMatching bool) (tokens, wildcards []string)
}
```

`DefaultTokenizer` is a package-level `*MixedTokenizer`. The package-level
functions `TokenizeForIndex` and `TokenizeForSearch` are thin
backward-compatible wrappers that delegate to it.

## Implementations

### `MixedTokenizer` (the default)

Handles text that may contain both ASCII and CJK content. It has a fast path: if
the input contains no CJK characters it goes straight to the ASCII tokenizer.
Otherwise it splits the input into contiguous CJK / non-CJK runs, tokenizes each
run with the appropriate tokenizer, then merges and de-duplicates. For indexing
the merged result is sorted; for search the original run order is preserved and
wildcards from every run are concatenated.

### `ASCIITokenizer`

Tokenizes Latin letters, digits, and common programming identifiers.

- Candidate words are matched with a regex covering identifier-like runs and
  dotted forms (e.g. version numbers).
- Each candidate is decomposed with `CamelSnakeSplit` (see below).
- Tokens are lower-cased and constrained to length 3–80; tokens outside that
  range are dropped.
- For indexing, results are sorted and **prefix-deduplicated**: if one token is
  a prefix of the next, the shorter one is dropped.
- For search with `exactMatching`, the raw regex matches are returned as-is with
  no wildcards. Otherwise, a `*` immediately before a token (e.g. `*abc-def`)
  marks the first fragment as a wildcard and indexes the remainder.

### `CJKTokenizer`

Segments CJK text using the [gse](https://github.com/go-ego/gse) segmenter.

- The gse dictionary is **lazily loaded once** via `sync.Once` on first CJK
  tokenization, so pure-ASCII workloads never pay the (substantial) dictionary
  load cost. See `BENCHMARK.md` for measured numbers.
- Segments are trimmed, lower-cased, and de-duplicated; pure
  whitespace/punctuation/symbol segments and CJK stop words are filtered out.
- For indexing, results are sorted; for search, segmentation order is preserved
  and the wildcards return value is always empty (`nil`).

## Supporting pieces

- **`CamelSnakeSplit(s)`** — splits an identifier into its progressively shorter
  suffixes at camelCase boundaries and `-`/`_`/`.` separators. For example
  `handleUpdateDocument` yields `handleUpdateDocument`, `UpdateDocument`,
  `Document`. Leading/trailing separators are trimmed and fragments shorter than
  3 characters terminate the split.
- **CJK detection (`isCJK` / `containsCJK`)** — classifies runes as CJK by
  Unicode script (Han, Hangul, Katakana, Hiragana). Used to choose the fast path
  and to split mixed text into runs.
- **Stopwords** — a hardcoded set of common Chinese function words (particles,
  conjunctions, prepositions, pronouns, adverbs, measure words, modal
  particles). The set is embedded in code to avoid any external file dependency.
  Stopword filtering applies only to CJK tokens; ASCII tokens bypass it.

## Index vs. search asymmetry

`TokenizeForIndex` aims for a compact, normalized, de-duplicated set suitable
for posting-list storage (sorting, prefix-dedup, length bounds).
`TokenizeForSearch` preserves what the query author typed more faithfully and
additionally surfaces wildcard fragments so the query engine can perform prefix
matching.
