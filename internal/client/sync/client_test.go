package sync

import "testing"

func TestParseJoinLink(t *testing.T) {
	tests := []struct {
		link, base, code string
		ok               bool
	}{
		{"https://pool.example.com/join/abc123", "https://pool.example.com", "abc123", true},
		{"  https://example.com/sharecodex/join/abc  ", "https://example.com/sharecodex", "abc", true},
		{"http://10.0.0.2:8080/join/xyz?utm=1", "http://10.0.0.2:8080", "xyz", true},
		{"https://example.com/join/", "", "", false},
		{"example.com/join/abc", "", "", false},
		{"ftp://example.com/join/abc", "", "", false},
	}
	for _, tt := range tests {
		base, code, err := ParseJoinLink(tt.link)
		if (err == nil) != tt.ok || base != tt.base || code != tt.code {
			t.Errorf("ParseJoinLink(%q) = %q, %q, %v", tt.link, base, code, err)
		}
	}
}
