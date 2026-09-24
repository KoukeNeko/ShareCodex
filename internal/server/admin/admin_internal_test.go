package admin

import "testing"

func TestCompactTokens(t *testing.T) {
	tests := map[int64]string{950: "950", 12_345: "12.3K", 4_500_000: "4.5M", 2_100_000_000: "2.1B"}
	for n, want := range tests {
		if got := compactTokens(n); got != want {
			t.Errorf("compactTokens(%d) = %q, want %q", n, got, want)
		}
	}
}
