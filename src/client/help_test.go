package client

import "testing"

func TestWantsHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, true},
		{"empty slice", []string{}, true},
		{"-h flag", []string{"-h"}, true},
		{"--help flag", []string{"--help"}, true},
		{"non-help arg", []string{"foo"}, false},
		{"help as non-first", []string{"foo", "-h"}, false},
		{"query with options", []string{"-limit", "10"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wantsHelp(tt.args)
			if got != tt.want {
				t.Errorf("wantsHelp(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
