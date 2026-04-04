package searcher

import (
	"testing"
)

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		expected string // Expected string representation after parsing
	}{
		// Basic term tests
		{
			name:     "single word",
			input:    "hello",
			wantErr:  false,
			expected: "hello",
		},
		{
			name:     "quoted phrase",
			input:    "\"hello world\"",
			wantErr:  false,
			expected: "\"hello world\"",
		},
		{
			name:     "wildcard term",
			input:    "hello*",
			wantErr:  false,
			expected: "hello*",
		},

		// Logical operator tests - AND
		{
			name:     "explicit AND operator",
			input:    "cat AND dog",
			wantErr:  false,
			expected: "cat AND dog",
		},
		{
			name:     "implicit AND (space)",
			input:    "cat   dog",
			wantErr:  false,
			expected: "cat dog",
		},
		{
			name:     "multiple AND operators",
			input:    "cat AND dog AND bird",
			wantErr:  false,
			expected: "cat AND dog AND bird",
		},
		{
			name:     "multiple implicit AND",
			input:    "cat   dog   bird",
			wantErr:  false,
			expected: "cat dog bird",
		},
		{
			name:     "mixed explicit and implicit AND",
			input:    "cat AND dog   bird",
			wantErr:  false,
			expected: "cat AND dog bird",
		},

		// Logical operator tests - OR
		{
			name:     "OR operator",
			input:    "cat OR dog",
			wantErr:  false,
			expected: "cat OR dog",
		},
		{
			name:     "pipe OR operator",
			input:    "cat | dog",
			wantErr:  false,
			expected: "cat | dog",
		},
		{
			name:     "multiple OR operators",
			input:    "cat OR dog OR bird",
			wantErr:  false,
			expected: "cat OR dog OR bird",
		},

		// Logical operator tests - NOT
		{
			name:     "NOT operator",
			input:    "NOT cat",
			wantErr:  false,
			expected: "NOT cat",
		},
		{
			name:     "NOT with AND",
			input:    "dog AND NOT cat",
			wantErr:  false,
			expected: "dog AND NOT cat",
		},

		// Precedence tests
		{
			name:     "AND precedence over OR",
			input:    "cat AND dog OR bird",
			wantErr:  false,
			expected: "cat AND dog OR bird",
		},
		{
			name:     "AND precedence with implicit AND",
			input:    "cat   dog OR bird",
			wantErr:  false,
			expected: "cat dog OR bird",
		},
		{
			name:     "OR with explicit AND",
			input:    "cat OR dog AND bird",
			wantErr:  false,
			expected: "cat OR dog AND bird",
		},
		{
			name:     "complex precedence",
			input:    "cat OR dog AND bird OR fish",
			wantErr:  false,
			expected: "cat OR dog AND bird OR fish",
		},

		// Wildcard tests
		{
			name:     "wildcard with operators",
			input:    "cat* AND dog*",
			wantErr:  false,
			expected: "cat* AND dog*",
		},
		{
			name:     "multiple wildcards in query",
			input:    "cat* OR dog* OR bird*",
			wantErr:  false,
			expected: "cat* OR dog* OR bird*",
		},

		// Case sensitivity tests
		{
			name:     "lowercase and",
			input:    "cat   and   dog",
			wantErr:  false,
			expected: "cat and dog", // "and" is treated as a term, not operator
		},
		{
			name:     "lowercase or",
			input:    "cat   or   dog",
			wantErr:  false,
			expected: "cat or dog", // "or" is treated as a term, not operator
		},
		{
			name:     "lowercase not",
			input:    "cat   not   dog",
			wantErr:  false,
			expected: "cat not dog", // "not" is treated as a term, not operator
		},
		{
			name:     "uppercase NOT",
			input:    "cat   NOT dog",
			wantErr:  false,
			expected: "cat NOT dog", // Here NOT is the operator
		},

		// Edge cases
		{
			name:     "empty query",
			input:    "",
			wantErr:  true,
			expected: "",
		},
		{
			name:     "only whitespace",
			input:    "   ",
			wantErr:  true,
			expected: "",
		},
		{
			name:     "unterminated quote",
			input:    "\"hello world",
			wantErr:  true,
			expected: "",
		},
		{
			name:     "standalone operator",
			input:    "AND",
			wantErr:  true,
			expected: "",
		},
		{
			name:     "consecutive operators",
			input:    "cat AND OR dog",
			wantErr:  true,
			expected: "",
		},

		// Complex queries
		{
			name:     "complex query with all operators",
			input:    "cat* AND dog OR NOT bird AND fish",
			wantErr:  false,
			expected: "cat* AND dog OR NOT bird AND fish",
		},
		{
			name:     "complex query with quotes and wildcards",
			input:    "\"cat food\"* AND dog OR \"fish\"",
			wantErr:  false,
			expected: "\"cat food\"* AND dog OR \"fish\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseQuery(tt.input)

			// Check error
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseQuery() error = %v, wantErr %v, input=%s", err, tt.wantErr, tt.input)
				return
			}

			// Skip further checks if we expected an error
			if tt.wantErr {
				return
			}

			// Check string representation
			got := result.String()
			if got != tt.expected {
				t.Errorf("ParseQuery() got = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestEdgeCases tests specific edge cases that need special validation beyond simple parsing
func TestEdgeCases(t *testing.T) {
	// Test with very long query
	longQuery := ""
	for i := 0; i < 100; i++ {
		longQuery += "term" + " AND "
	}
	longQuery += "lastterm"

	result, err := ParseQuery(longQuery)
	if err != nil {
		t.Errorf("ParseQuery() failed on long query: %v", err)
	} else if result == nil {
		t.Errorf("ParseQuery() returned nil for long query")
	}

	// Test with malformed wildcards (if these are supposed to be invalid)
	wildcardTests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"wildcard at beginning", "*hello", true},
		{"standalone wildcard", "*", true},
		{"multiple wildcards", "hello**", true},
	}

	for _, tt := range wildcardTests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseQuery(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseQuery() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestPrecedence specifically tests operator precedence
func TestPrecedence(t *testing.T) {
	// This query should parse as: (cat AND dog) OR (fish AND bird)
	// due to AND having higher precedence than OR
	query := "cat AND dog OR fish AND bird"

	result, err := ParseQuery(query)
	if err != nil {
		t.Fatalf("ParseQuery() error = %v", err)
	}

	// This is a more thorough precedence test that would require examining
	// the actual AST structure. For now we're just checking that it parses.
	if result == nil {
		t.Errorf("ParseQuery() returned nil")
	}
}
