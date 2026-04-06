package tokenizer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsCJK(t *testing.T) {
	t.Run("Chinese character", func(t *testing.T) {
		assert.True(t, isCJK('中'))
	})
	t.Run("Japanese Hiragana", func(t *testing.T) {
		assert.True(t, isCJK('あ'))
	})
	t.Run("Japanese Katakana", func(t *testing.T) {
		assert.True(t, isCJK('ア'))
	})
	t.Run("Korean Hangul", func(t *testing.T) {
		assert.True(t, isCJK('한'))
	})
	t.Run("ASCII letter", func(t *testing.T) {
		assert.False(t, isCJK('A'))
	})
	t.Run("ASCII digit", func(t *testing.T) {
		assert.False(t, isCJK('1'))
	})
	t.Run("Space", func(t *testing.T) {
		assert.False(t, isCJK(' '))
	})
	t.Run("Latin accented character", func(t *testing.T) {
		assert.False(t, isCJK('é'))
	})
}

func TestContainsCJK(t *testing.T) {
	t.Run("Pure Chinese", func(t *testing.T) {
		assert.True(t, containsCJK("测试中文"))
	})
	t.Run("Pure Japanese Hiragana", func(t *testing.T) {
		assert.True(t, containsCJK("ひらがな"))
	})
	t.Run("Pure Japanese Katakana", func(t *testing.T) {
		assert.True(t, containsCJK("カタカナ"))
	})
	t.Run("Mixed Japanese", func(t *testing.T) {
		assert.True(t, containsCJK("漢字とひらがな"))
	})
	t.Run("Pure Korean", func(t *testing.T) {
		assert.True(t, containsCJK("한국어"))
	})
	t.Run("Chinese mixed with ASCII", func(t *testing.T) {
		assert.True(t, containsCJK("hello世界"))
	})
	t.Run("Korean mixed with ASCII", func(t *testing.T) {
		assert.True(t, containsCJK("test한글test"))
	})
	t.Run("Japanese mixed with ASCII", func(t *testing.T) {
		assert.True(t, containsCJK("testあいう"))
	})
	t.Run("CJK at end of string", func(t *testing.T) {
		assert.True(t, containsCJK("abc中"))
	})
	t.Run("CJK at start of string", func(t *testing.T) {
		assert.True(t, containsCJK("中abc"))
	})
	t.Run("Single CJK character", func(t *testing.T) {
		assert.True(t, containsCJK("中"))
	})

	// Negative cases
	t.Run("Pure ASCII", func(t *testing.T) {
		assert.False(t, containsCJK("hello world"))
	})
	t.Run("ASCII with numbers", func(t *testing.T) {
		assert.False(t, containsCJK("test123"))
	})
	t.Run("ASCII with special chars", func(t *testing.T) {
		assert.False(t, containsCJK("hello@world.com"))
	})
	t.Run("Empty string", func(t *testing.T) {
		assert.False(t, containsCJK(""))
	})
	t.Run("Only spaces", func(t *testing.T) {
		assert.False(t, containsCJK("   "))
	})
	t.Run("Latin accented characters", func(t *testing.T) {
		assert.False(t, containsCJK("café résumé"))
	})
	t.Run("Cyrillic", func(t *testing.T) {
		assert.False(t, containsCJK("Привет"))
	})
	t.Run("Code identifiers", func(t *testing.T) {
		assert.False(t, containsCJK("handleUpdateDocument"))
	})
}
