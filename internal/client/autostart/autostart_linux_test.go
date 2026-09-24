package autostart

import "testing"

func TestUnitQuotesValues(t *testing.T) {
	got := unit(`/home/a b/sharecodex`, map[string]string{"PATH": `/usr/bin:/home/a/100%"x"`, "CODEX_HOME": "/home/a/.codex"})
	want := `[Unit]
Description=ShareCodex agent
After=network-online.target

[Service]
ExecStart="/home/a b/sharecodex" agent
Environment="PATH=/usr/bin:/home/a/100%%\"x\""
Environment="CODEX_HOME=/home/a/.codex"
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
`
	if got != want {
		t.Errorf("unit =\n%s\nwant\n%s", got, want)
	}
}
