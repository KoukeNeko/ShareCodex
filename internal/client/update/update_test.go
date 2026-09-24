package update

import "testing"

func TestNewer(t *testing.T) {
	tests := []struct {
		candidate, current string
		want               bool
	}{
		{"v0.2.0", "0.1.9", true},
		{"v0.1.10", "0.1.9", true},
		{"v0.1.0", "0.1.0", false},
		{"v0.1.0", "0.2.0", false},
		{"v1.0.0-beta.1", "0.9.0", true},
		{"v1.0", "1.0.0", false},
	}
	for _, tt := range tests {
		if got := Newer(tt.candidate, tt.current); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.candidate, tt.current, got, tt.want)
		}
	}
}
