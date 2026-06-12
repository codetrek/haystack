# engine

Package `engine` is the content query engine of searchcore. It turns a free-form
query string into matches against indexed content in three stages:

1. **Compile** — parse the query into OR/AND/term clauses and build a regular
   expression per clause.
2. **Collect candidates** — use the inverted index to narrow the search to the
   set of document ids whose keywords could match.
3. **Line match** — given the actual content of a candidate document, test each
   line against the compiled regex and return the exact match offsets.

The engine is deliberately split this way: the index phase is a cheap, coarse
filter over keywords; the regex phase is the precise (and more expensive) match,
run by the caller only over the lines of documents that survived the filter.
The `engine` package does **not** read file content itself — the caller fetches
the content for each candidate document and feeds lines to `IsLineMatch`.

It is a standalone library package: it has no dependency on `internal/`.

## Place in the layering

```
engine    ← this package: content query engine (reads index + documents)
   │
   ├── invertedindex   (candidate collection: term → doc ids)
   └── documents       (resolve a collection's inverted-index table)
```

`engine` sits beside the storage stack rather than within it: it depends on
`invertedindex`, `documents`, and `tokenizer`, but nothing in those packages
depends on `engine`. An `Engine` is a short-lived, per-query object bound to one
collection id.

## Key types

- **`Engine`** (`engine.go`) — constructed with
  `New(idx, docs, collectionID, opts)` and bound to a single collection. `idx`
  and `docs` may be `nil` for unit tests that only call `Compile` / `IsLineMatch`
  without `CollectDocuments`. Holds the compiled OR-clauses after `Compile`.
- **`Options`** (`engine.go`) — matching tunables:
  - `MaxWildcardLength` — max characters a `*` may expand to in the regex.
  - `MaxKeywordDistance` — max characters allowed between adjacent AND terms.
  - `WholeWord` — when true, wraps each term in `\b` word boundaries.
- **`andClause` / `term`** (unexported, `engine.go`) — the internal compiled
  representation: one `andClause` per OR branch, holding the branch's compiled
  regex and its AND `term`s; each `term` carries the raw pattern, its regex
  rendering, and the index `keywords` and `wildcards` derived via
  `tokenizer.TokenizeForSearch`.

## The three phases

### Compile

`Compile(query, caseSensitive)` tokenizes the query while preserving
double-quoted phrases as single tokens (`TokenizeWithQuotes`). `|` separates OR
clauses; whitespace (and the literal `AND`) separates AND terms within a clause.
Each token becomes a `term`: quoted phrases are matched literally (regex-escaped),
bare tokens have their regex metacharacters escaped and `*` expanded to a bounded
`.{0,MaxWildcardLength}`. Per OR clause, the AND terms are joined into one regex
with `.{0,MaxKeywordDistance}` between them, optionally case-insensitive
(`(?i)`) and optionally word-bounded.

### Collect candidates

`CollectDocuments()` resolves the collection's inverted-index table (via
`documents.Store.GetCollection`) and queries it per term:

- Within an AND clause, results are **intersected** across terms (a candidate
  must contain every term's keywords). Each term with multiple keywords is itself
  the intersection of those keyword lookups.
- Across OR clauses, results are **unioned**.
- Wildcard-derived ids are tracked separately as `WildDocIds` and kept only when
  they also appear in the exact `DocIds` set.

The result is an `invertedindex.SearchResult` — a set of candidate document ids,
not confirmed matches.

### Line match

`IsLineMatch(line)` runs the compiled OR-clause regexes against a single line and
returns `[][]int` byte-offset `[start, end]` pairs (suitable for highlighting),
returning the matches from the first clause that matches. The caller iterates the
candidate documents from `CollectDocuments`, reads their content, and calls
`IsLineMatch` per line to produce the final, verified results.

## Helpers

`TokenizeWithQuotes`, `IsQuotedPhrase`, and `UnwrapQuotes` (exported in
`engine.go`) handle quote-aware tokenization and are reused by the compiler.
`Engine.String()` renders the compiled query (`term AND term | term`) for logging.
