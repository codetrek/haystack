package prompts

import (
	"testing"
	"time"

	"github.com/ai-microsoft/haystack/utils/queue"
)

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a        []float32
		b        []float32
		expected float32
		panics   bool
	}{
		{
			name:     "Identical vectors",
			a:        []float32{1, 2, 3},
			b:        []float32{1, 2, 3},
			expected: 1.0,
		},
		{
			name:     "Orthogonal vectors",
			a:        []float32{1, 0},
			b:        []float32{0, 1},
			expected: 0.0,
		},
		{
			name:     "Opposite vectors",
			a:        []float32{1, 0},
			b:        []float32{-1, 0},
			expected: -1.0,
		},
		{
			name:     "Zero vector",
			a:        []float32{0, 0, 0},
			b:        []float32{1, 2, 3},
			expected: 0.0,
		},
		{
			name:     "Both zero vectors",
			a:        []float32{0, 0},
			b:        []float32{0, 0},
			expected: 0.0,
		},
		{
			name:   "Different lengths",
			a:      []float32{1, 2},
			b:      []float32{1, 2, 3},
			panics: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.panics {
				defer func() {
					if r := recover(); r == nil {
						t.Error("Expected panic but got none")
					}
				}()
			}

			result := CosineSimilarity(tt.a, tt.b)

			if !tt.panics {
				// Allow small floating point differences
				if abs(result-tt.expected) > 1e-6 {
					t.Errorf("CosineSimilarity(%v, %v) = %f, want %f", tt.a, tt.b, result, tt.expected)
				}
			}
		})
	}
}

func abs(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

func TestGetThresholdByQueryLength(t *testing.T) {
	tests := []struct {
		name      string
		wordCount int
		expected  float32
	}{
		{"One word", 1, 0.5},
		{"Two words", 2, 0.5},
		{"Three words", 3, 0.65},
		{"Five words", 5, 0.65},
		{"Six words", 6, 0.75},
		{"Ten words", 10, 0.75},
		{"Zero words", 0, 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetThresholdByQueryLength(tt.wordCount)
			if result != tt.expected {
				t.Errorf("GetThresholdByQueryLength(%d) = %f, want %f", tt.wordCount, result, tt.expected)
			}
		})
	}
}

func TestExtractDescriptionFromPrompt(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name: "Description with double quotes",
			content: `---
title: Test Prompt
description: "This is a test description"
---
Some content here`,
			expected: "This is a test description",
		},
		{
			name: "Description with single quotes",
			content: `---
title: Test Prompt
description: 'This is a test description'
---
Some content here`,
			expected: "This is a test description",
		},
		{
			name: "Description without quotes",
			content: `---
title: Test Prompt
description: This is a test description
---
Some content here`,
			expected: "This is a test description",
		}, {
			name: "Description with multiline format (>)",
			content: `---
title: Test Prompt
description: >
  This is a multiline description
---
Some content here`,
			expected: ">",
		},
		{
			name: "Description with multiline format (|)",
			content: `---
title: Test Prompt
description: |
  This is a multiline description
---
Some content here`,
			expected: "|",
		},
		{
			name: "No front matter",
			content: `This is just regular content
without any front matter`,
			expected: "",
		},
		{
			name: "Front matter without description",
			content: `---
title: Test Prompt
author: Someone
---
Some content here`,
			expected: "",
		}, {
			name: "Empty description",
			content: `---
title: Test Prompt
description: ""
---
Some content here`,
			expected: "\"\"",
		},
		{
			name: "Front matter at end",
			content: `Some content first
---
title: Test Prompt
description: "This should be found"
---`,
			expected: "This should be found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractDescriptionFromPrompt(tt.content)
			if result != tt.expected {
				t.Errorf("ExtractDescriptionFromPrompt() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestSavePrompts(t *testing.T) {
	// Setup mock database
	mockDB := &MockDB{
		data: make(map[string][]byte),
	}

	// Initialize the package with mock database and mpsc
	mockMpsc := queue.NewMpsc("test")
	mockMpsc.Start()
	defer func() {
		CloseAndWait()
		mockMpsc.Stop()
	}()

	err := Init(mockDB, mockMpsc)
	if err != nil {
		t.Fatalf("Failed to initialize prompts package: %v", err)
	}

	// Create test embedding data
	workspaceId := 123
	testEmbedding1 := []float32{0.1, 0.2, 0.3, 0.4}
	testEmbedding2 := []float32{0.5, 0.6, 0.7, 0.8}

	// Encode embeddings to byte arrays
	embedding1Bytes, err := EncodeFloat32Vector(testEmbedding1)
	if err != nil {
		t.Fatalf("Failed to encode test embedding 1: %v", err)
	}

	embedding2Bytes, err := EncodeFloat32Vector(testEmbedding2)
	if err != nil {
		t.Fatalf("Failed to encode test embedding 2: %v", err)
	}

	// Create test prompt embedding data
	promptsToSave := []PromptEmbeddingData{
		{
			Key:   EncodePromptPathKey(workspaceId, "test1.prompt.md"),
			Value: embedding1Bytes,
		},
		{
			Key:   EncodePromptPathKey(workspaceId, "subdir/test2.prompt.md"),
			Value: embedding2Bytes,
		},
	}

	// Test SavePrompts function
	SavePrompts(promptsToSave)

	// Wait a brief moment for the async operation to complete
	time.Sleep(100 * time.Millisecond)

	// Verify that the data was saved correctly
	expectedKeys := []string{
		string(EncodePromptPathKey(workspaceId, "test1.prompt.md")),
		string(EncodePromptPathKey(workspaceId, "subdir/test2.prompt.md")),
	}

	for i, expectedKey := range expectedKeys {
		if savedValue, exists := mockDB.data[expectedKey]; !exists {
			t.Errorf("Expected key %s not found in database", expectedKey)
		} else {
			// Verify the saved value matches what we encoded
			expectedValue := promptsToSave[i].Value
			if !bytesEqual(savedValue, expectedValue) {
				t.Errorf("Saved value for key %s does not match expected value", expectedKey)
			}
		}
	}

	// Verify we have exactly the expected number of entries
	if len(mockDB.data) != len(expectedKeys) {
		t.Errorf("Expected %d entries in database, got %d", len(expectedKeys), len(mockDB.data))
	}
}

// Helper function to compare byte slices
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
