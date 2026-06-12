package tokenizer

// chineseStopWords contains common Chinese function words (虚词) that carry
// little semantic meaning and should be excluded from index and search tokens.
// The set is intentionally hardcoded to avoid any external file dependency.
var chineseStopWords = map[string]struct{}{
	// ── 助词 (Particles) ──
	"的": {},
	"地": {},
	"得": {},
	"了": {},
	"着": {},
	"过": {},
	"之": {},
	"所": {},

	// ── 连词 (Conjunctions) ──
	"和":  {},
	"与":  {},
	"及":  {},
	"或":  {},
	"但":  {},
	"而":  {},
	"因为": {},
	"所以": {},
	"如果": {},
	"虽然": {},
	"但是": {},
	"而且": {},
	"或者": {},
	"并且": {},
	"因此": {},
	"于是": {},

	// ── 介词 (Prepositions) ──
	"在":  {},
	"从":  {},
	"向":  {},
	"到":  {},
	"对":  {},
	"把":  {},
	"被":  {},
	"让":  {},
	"给":  {},
	"用":  {},
	"以":  {},
	"为":  {},
	"按":  {},
	"关于": {},

	// ── 代词 (Pronouns) ──
	"我":  {},
	"你":  {},
	"他":  {},
	"她":  {},
	"它":  {},
	"我们": {},
	"你们": {},
	"他们": {},
	"她们": {},
	"它们": {},
	"自己": {},
	"这":  {},
	"那":  {},
	"这个": {},
	"那个": {},
	"这些": {},
	"那些": {},
	"这里": {},
	"那里": {},
	"哪":  {},
	"谁":  {},
	"什么": {},
	"怎么": {},
	"多少": {},

	// ── 副词 (Adverbs) ──
	"不":  {},
	"没":  {},
	"没有": {},
	"很":  {},
	"也":  {},
	"都":  {},
	"就":  {},
	"才":  {},
	"还":  {},
	"又":  {},
	"再":  {},
	"已":  {},
	"已经": {},
	"正在": {},
	"将":  {},
	"会":  {},
	"能":  {},
	"可以": {},
	"应该": {},
	"必须": {},
	"可能": {},
	"非常": {},
	"十分": {},
	"比较": {},
	"最":  {},
	"更":  {},
	"太":  {},
	"真":  {},
	"只":  {},

	// ── 量词 (Measure words) ──
	"个": {},
	// "只" is listed above under adverbs (it serves both roles).
	"条": {},
	"些": {},

	// ── 语气词 (Modal particles) ──
	"吗": {},
	"呢": {},
	"吧": {},
	"啊": {},
	"呀": {},
	"哦": {},
	"嘛": {},
	"啦": {},

	// ── 其他虚词 (Other function words) ──
	"是":  {},
	"有":  {},
	"就是": {},
	"这样": {},
	"那样": {},
	"如何": {},
	"怎样": {},
	"上":  {},
	"下":  {},
	"来":  {},
	"去":  {},
}

// isStopWord returns true if the given token is a Chinese stop word.
// Only CJK tokens should be checked against this list; ASCII tokens
// must bypass stop-word filtering entirely.
func isStopWord(token string) bool {
	_, ok := chineseStopWords[token]
	return ok
}
