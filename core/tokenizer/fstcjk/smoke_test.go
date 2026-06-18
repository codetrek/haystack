package fstcjk

import (
	"reflect"
	"testing"
)

// TestSmokeGolden is the always-on, gse-FREE correctness check. It proves the
// committed dict.fst is intact and wired through the production segmenter
// (Open -> getDag -> calc -> CutDAG -> hmm OOV fallback), asserting against
// frozen golden outputs captured from the byte-fidelity-proven segmenter.
//
// Unlike the live-gse fidelity suite (gated behind HAYSTACK_FIDELITY), this runs
// on every CI: it loads no 8.6MB gse dictionary and does no fuzzing, so it is
// ~instant. The exhaustive FST-vs-gse proof only needs to run when dict.fst, the
// segmenter, or the gse version changes — see skipUnlessFidelity.
//
// If a golden here changes, the segmentation output changed: regenerate dict.fst
// / re-run the HAYSTACK_FIDELITY suite and update these expectations deliberately.
func TestSmokeGolden(t *testing.T) {
	s, err := Open()
	if err != nil {
		t.Fatalf("Open embedded FST: %v", err)
	}
	cases := []struct {
		in   string
		want []string
	}{
		{"人工智能正在改变世界", []string{"人工智能", "正在", "改变", "世界"}},
		{"机器学习和深度学习", []string{"机器", "学习", "和", "深度", "学习"}},
		{"北京大学计算机科学", []string{"北京大学", "计算机科学"}},
		{"Go语言1.24版本发布", []string{"go", "语言", "1.24", "版本", "发布"}}, // ASCII+digit+CJK mix
		{"iPhone手机很贵", []string{"iphone", "手机", "很贵"}},
		{"数据分析师的工作", []string{"数据", "分析师", "的", "工作"}},
		{"区块链技术应用", []string{"区块", "链", "技术", "应用"}},
		{"新能源汽车销量", []string{"新能源", "汽车销量"}},
		{"他在GitHub开源了项目", []string{"他", "在", "github", "开源", "了", "项目"}},
		{"中国", []string{"中国"}},
		{"水", []string{"水"}},
		{"的", []string{"的"}},
		{"螺蛳粉很好吃", []string{"螺蛳", "粉", "很", "好吃"}},
		{"元宇宙概念", []string{"元", "宇宙", "概念"}},
		{"碳达峰碳中和目标", []string{"碳达峰", "碳", "中和", "目标"}},
		{"鬱鬱蔥蔥的森林", []string{"鬱鬱蔥蔥", "的", "森林"}}, // traditional/OOV -> HMM
		{"曌曌曈曈", []string{"曌", "曌", "曈曈"}},       // rare chars -> OOV HMM run
		{"PD-1抑制剂", []string{"pd", "-", "1", "抑制剂"}},
		{"！？，。", []string{"！？，。"}}, // pure punctuation
		{"abc123", []string{"abc123"}},
	}
	for _, c := range cases {
		got := s.Cut(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Cut(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
