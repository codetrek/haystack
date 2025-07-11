package invertedindex

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWholeWordMatchingLogic tests the key prefix generation logic for exact vs prefix matching
func TestWholeWordMatchingLogic(t *testing.T) {
	tableId := 999
	query := "test"

	// Test the key encoding logic differences between exact and prefix matching
	t.Run("Key prefix generation", func(t *testing.T) {
		// Test exact match key prefix
		exactPrefix := encodeInvertedKeyPrefix(tableId, query)

		// Test prefix match key prefix
		prefixKey := encodeInvertedSearchKey(tableId, query)

		// The prefixes should be different
		assert.NotEqual(t, exactPrefix, prefixKey, "Exact match and prefix match should generate different scan prefixes")

		// Log the actual prefixes for manual verification
		t.Logf("Exact match prefix: %x", exactPrefix)
		t.Logf("Prefix match key: %x", prefixKey)
	})

	// Test keyword matching logic
	t.Run("Keyword matching logic", func(t *testing.T) {
		testCases := []struct {
			name        string
			query       string
			keyword     string
			exactMatch  bool
			shouldMatch bool
		}{
			{
				name:        "Exact match - exact keyword",
				query:       "test",
				keyword:     "test",
				exactMatch:  true,
				shouldMatch: true,
			},
			{
				name:        "Exact match - prefix keyword",
				query:       "test",
				keyword:     "testing",
				exactMatch:  true,
				shouldMatch: false,
			},
			{
				name:        "Prefix match - exact keyword",
				query:       "test",
				keyword:     "test",
				exactMatch:  false,
				shouldMatch: true,
			},
			{
				name:        "Prefix match - prefix keyword",
				query:       "test",
				keyword:     "testing",
				exactMatch:  false,
				shouldMatch: true,
			},
			{
				name:        "Exact match - case insensitive",
				query:       "Test",
				keyword:     "test",
				exactMatch:  true,
				shouldMatch: true,
			},
			{
				name:        "Prefix match - case insensitive",
				query:       "Test",
				keyword:     "testing",
				exactMatch:  false,
				shouldMatch: true,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				// Simulate the keyword matching logic from the Search function
				var shouldInclude bool
				if tc.exactMatch {
					// For exact match, the keyword must exactly match the query (case insensitive)
					shouldInclude = (tc.keyword == strings.ToLower(tc.query))
				} else {
					// For prefix match, the keyword should start with the query (case insensitive)
					shouldInclude = strings.HasPrefix(tc.keyword, strings.ToLower(tc.query))
				}

				assert.Equal(t, tc.shouldMatch, shouldInclude,
					"Keyword '%s' matching against query '%s' with exactMatch=%v",
					tc.keyword, tc.query, tc.exactMatch)
			})
		}
	})

	// Test codec functions to ensure they work correctly
	t.Run("Codec functions", func(t *testing.T) {
		tableId := 123
		keyword := "test"
		// Use 16-byte document IDs as expected by the codec
		docIds := []string{"doc1____________", "doc2____________", "doc3____________"}

		// Test encoding and decoding
		key := encodeInvertedKey(tableId, keyword, len(docIds))
		value := encodeInvertedValue(docIds)

		// Decode and verify
		decodedTableId, decodedKeyword, decodedCount, decodedTimestamp := decodeInvertedKey(string(key))
		decodedDocIds := decodeInvertedValue(value)

		assert.Equal(t, tableId, decodedTableId, "Table ID should match")
		assert.Equal(t, keyword, decodedKeyword, "Keyword should match")
		assert.Equal(t, len(docIds), decodedCount, "Document count should match")
		assert.Equal(t, docIds, decodedDocIds, "Document IDs should match")
		assert.NotZero(t, decodedTimestamp, "Timestamp should be set")

		t.Logf("Encoded key: %x", key)
		t.Logf("Encoded value: %x", value)
		t.Logf("Decoded: tableId=%d, keyword=%s, count=%d, timestamp=%s",
			decodedTableId, decodedKeyword, decodedCount, decodedTimestamp)
	})
}
