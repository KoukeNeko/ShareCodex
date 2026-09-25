package statusline

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/quota"
)

const sample = `{"session_id":"s1","model":{"display_name":"Opus"},
 "rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1790000000},"seven_day":{"used_percentage":41.2,"resets_at":1790500000}}}`

func TestRunSpoolsAndChains(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chained command uses a POSIX shell")
	}
	dir := t.TempDir()
	var out, errOut bytes.Buffer

	Run(strings.NewReader(sample), &out, &errOut, dir, `cat >/dev/null; printf mine`)

	if out.String() != "mine" {
		t.Errorf("chained output = %q, want %q", out.String(), "mine")
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected stderr: %s", errOut.String())
	}
	spooled, err := ReadSpool(dir)
	if err != nil || len(spooled) != 1 {
		t.Fatalf("ReadSpool = %v, %v; want 1 snapshot", spooled, err)
	}
	if spooled[0].SessionID != "s1" {
		t.Errorf("session = %q, want s1 so the caller can tell which account it ran under", spooled[0].SessionID)
	}
	latest := quota.Latest([]quota.Snapshot{spooled[0].Snapshot})
	if p := *latest[quota.BucketWeekly].UsedPercent; p != 41.2 {
		t.Errorf("weekly = %v, want 41.2", p)
	}
}

func TestSpoolSkipsUnchangedLimits(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Unix(1000, 0)
	if err := spool([]byte(sample), dir, t0); err != nil {
		t.Fatal(err)
	}
	if err := spool([]byte(sample), dir, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	spooled, _ := ReadSpool(dir)
	if !spooled[0].Snapshot.ObservedAt.Equal(t0) {
		t.Errorf("unchanged limits rewrote the spool: observed %v, want %v", spooled[0].Snapshot.ObservedAt, t0)
	}
}

func TestSpoolIgnoresInputWithoutLimits(t *testing.T) {
	dir := t.TempDir()
	if err := spool([]byte(`{"session_id":"s2"}`), dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("spooled %d files for input without rate limits", len(entries))
	}
	if err := spool([]byte(`{"session_id":"../x","rate_limits":{}}`), dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "x.json")); err == nil {
		t.Error("session ID with a path separator escaped the spool directory")
	}
}
