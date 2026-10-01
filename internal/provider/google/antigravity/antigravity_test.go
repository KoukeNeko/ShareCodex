package antigravity

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

func message(fields ...func([]byte) []byte) []byte {
	var b []byte
	for _, f := range fields {
		b = f(b)
	}
	return b
}

func varint(num protowire.Number, v uint64) func([]byte) []byte {
	return func(b []byte) []byte {
		return protowire.AppendVarint(protowire.AppendTag(b, num, protowire.VarintType), v)
	}
}

func bytesField(num protowire.Number, v []byte) func([]byte) []byte {
	return func(b []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(b, num, protowire.BytesType), v)
	}
}

// generation lays out a gen_metadata row as agy writes it.
func generationRow(model string, step string, stats []byte) []byte {
	response := message(
		bytesField(responseModel, []byte(model)),
		bytesField(responseLabels, message(bytesField(labelKey, []byte("used_claude")), bytesField(labelValue, []byte("false")))),
		bytesField(responseLabels, message(bytesField(labelKey, []byte(stepIndexLabel)), bytesField(labelValue, []byte(step)))),
	)
	if stats != nil {
		response = message(func(b []byte) []byte { return append(b, response...) }, bytesField(responseUsage, stats))
	}
	return message(bytesField(genResponse, response))
}

func stepRow(at time.Time) []byte {
	return message(bytesField(stepMetadataTime, message(varint(1, uint64(at.Unix())), varint(2, uint64(at.Nanosecond())))))
}

func TestParseConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conv-1.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE steps (idx integer PRIMARY KEY, step_type integer, metadata blob)",
		"CREATE TABLE gen_metadata (idx integer PRIMARY KEY, data blob, size integer)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Date(2026, 10, 1, 9, 16, 50, 0, time.UTC)
	steps := map[int]time.Time{0: t0, 1: t0.Add(5 * time.Second), 2: t0.Add(time.Minute), 3: t0.Add(2 * time.Minute)}
	for idx, at := range steps {
		if _, err := db.Exec("INSERT INTO steps VALUES (?, 15, ?)", idx, stepRow(at)); err != nil {
			t.Fatal(err)
		}
	}
	gens := [][]byte{
		// The first request: 15574 input tokens, 20 output of which 19
		// thinking, as agy's own JSON output reports it.
		generationRow("gemini-3.8-flash", "0", message(varint(1, 1320), varint(usageInputTokens, 15574), varint(usageOutputTokens, 20),
			varint(6, 24), varint(usageThinkingTokens, 19))),
		// A later one read most of its prompt from the cache.
		generationRow("gemini-3.8-flash", "2", message(varint(usageInputTokens, 5369), varint(usageOutputTokens, 866),
			varint(usageCacheRead, 12208), varint(usageCacheWrite, 40), varint(usageThinkingTokens, 433))),
		// A request that failed reports no usage.
		generationRow("gemini-3.8-flash", "3", nil),
	}
	for idx, g := range gens {
		if _, err := db.Exec("INSERT INTO gen_metadata VALUES (?, ?, ?)", idx, g, len(g)); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	events, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []usage.Event{
		{DedupeKey: "antigravity:conv-1:0", Provider: account.ProviderGoogle, Product: usage.ProductAntigravity, SessionID: "conv-1",
			Model: "gemini-3.8-flash", OccurredAt: steps[0], Tokens: usage.Tokens{Input: 15574, Output: 20, ReasoningOutput: 19}},
		{DedupeKey: "antigravity:conv-1:1", Provider: account.ProviderGoogle, Product: usage.ProductAntigravity, SessionID: "conv-1",
			Model: "gemini-3.8-flash", OccurredAt: steps[2], Tokens: usage.Tokens{Input: 5369, CachedInput: 12208, CacheWrite: 40, Output: 866, ReasoningOutput: 433}},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %+v, want %d", events, len(want))
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, events[i], want[i])
		}
	}

	// A new request lands in the -wal file first; it must still count as
	// a change to the conversation.
	files, err := List([]string{filepath.Dir(path), filepath.Join(t.TempDir(), "missing")})
	if err != nil || len(files) != 1 {
		t.Fatalf("List = %v, %v", files, err)
	}
	before := files[path]
	if err := os.WriteFile(path+"-wal", make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	files, _ = List([]string{filepath.Dir(path)})
	if files[path].Size != before.Size+100 {
		t.Errorf("state with WAL = %+v, want the WAL's size added to %+v", files[path], before)
	}
}

func TestObserve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("PATH", "")
	if _, err := Observe(); err == nil {
		t.Fatal("without agy or its data, Observe must report it not installed")
	}
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(filepath.Join(dir, "antigravity-cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	o, err := Observe()
	if err != nil || o.ExternalRefHash != "" {
		t.Fatalf("signed out = %+v, %v; want installed with no account", o, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "google_accounts.json"), []byte(`{"active":"Alice@example.com","old":["bob@example.com"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	o, err = Observe()
	if err != nil || o.Hint != "Al***@example.com" || o.ExternalRefHash != account.HashExternalRef(account.ProviderGoogle, "alice@example.com") {
		t.Fatalf("signed in = %+v, %v", o, err)
	}
}
