package tokenizer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsStopWord(t *testing.T) {
	t.Run("Common stop words are filtered", func(t *testing.T) {
		stopWords := []string{
			// 助词
			"的", "地", "得", "了", "着", "过",
			// 连词
			"和", "与", "但", "而", "因为", "所以",
			// 介词
			"在", "从", "对", "把", "被", "给",
			// 代词
			"我", "你", "他", "她", "它", "我们", "他们", "这", "那", "这个", "那个",
			// 副词
			"不", "没", "很", "也", "都", "就", "还",
			// 量词
			"个", "只", "条", "些",
			// 语气词
			"吗", "呢", "吧", "啊",
			// 其他
			"是", "有",
		}
		for _, w := range stopWords {
			assert.True(t, isStopWord(w), "expected %q to be a stop word", w)
		}
	})

	t.Run("Content words are not filtered", func(t *testing.T) {
		contentWords := []string{
			"世界", "中国", "语言", "编程", "自然",
			"处理", "测试", "数据", "算法", "搜索",
			"电脑", "手机", "学习", "工作", "朋友",
		}
		for _, w := range contentWords {
			assert.False(t, isStopWord(w), "expected %q to NOT be a stop word", w)
		}
	})

	t.Run("ASCII tokens are not stop words", func(t *testing.T) {
		asciiTokens := []string{
			"hello", "world", "test", "golang", "search",
			"the", "is", "a", "an", "of", // English stop words should NOT be filtered
		}
		for _, w := range asciiTokens {
			assert.False(t, isStopWord(w), "ASCII token %q should not be a stop word", w)
		}
	})

	t.Run("Empty string is not a stop word", func(t *testing.T) {
		assert.False(t, isStopWord(""))
	})

	t.Run("Multi-character stop words", func(t *testing.T) {
		multiChar := []string{
			"因为", "所以", "如果", "虽然", "但是",
			"而且", "或者", "我们", "你们", "他们",
			"已经", "正在", "可以", "应该", "必须",
		}
		for _, w := range multiChar {
			assert.True(t, isStopWord(w), "expected multi-char %q to be a stop word", w)
		}
	})

	t.Run("Exact match only - substrings and superstrings are not matched", func(t *testing.T) {
		// "的" is a stop word, but "的确" should not be
		assert.True(t, isStopWord("的"))
		assert.False(t, isStopWord("的确"))

		// "我" is a stop word, but "我国" should not be
		assert.True(t, isStopWord("我"))
		assert.False(t, isStopWord("我国"))

		// "在" is a stop word, but "存在" should not be
		assert.True(t, isStopWord("在"))
		assert.False(t, isStopWord("存在"))
	})
}
