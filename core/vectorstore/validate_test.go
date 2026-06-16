package vectorstore

import (
	"math"
	"testing"
)

func TestValidateVector(t *testing.T) {
	tests := []struct {
		name    string
		v       []float32
		dim     int
		metric  Metric
		wantErr bool
	}{
		{"ok cosine", []float32{1, 2, 3}, 3, Cosine, false},
		{"ok dim-learn (dim 0)", []float32{1, 2}, 0, Cosine, false},
		{"empty", []float32{}, 3, Cosine, true},
		{"dim mismatch", []float32{1, 2}, 3, Cosine, true},
		{"cosine NaN", []float32{float32(math.NaN()), 1}, 2, Cosine, true},
		{"cosine Inf", []float32{float32(math.Inf(1)), 1}, 2, Cosine, true},
		{"cosine zero-norm ok", []float32{0, 0}, 2, Cosine, false},
		{"dot NaN allowed (no norm check)", []float32{float32(math.NaN()), 1}, 2, DotProduct, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateVector(tc.v, tc.dim, tc.metric)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateVector(%v, dim=%d, %v) err=%v, wantErr=%v", tc.v, tc.dim, tc.metric, err, tc.wantErr)
			}
		})
	}
}
