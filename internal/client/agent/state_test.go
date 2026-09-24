package agent

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
	"github.com/KoukeNeko/ShareCodex/internal/client/update"
)

func TestStateCarriesLanguageAndUpdate(t *testing.T) {
	t.Setenv("SHARECODEX_HOME", t.TempDir())
	ctx := context.Background()
	a, err := New(ctx, "0.1.0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	st := a.State(ctx)
	if st.Language != "en" || st.Update != nil {
		t.Fatalf("fresh state = language %q, update %v; want English and no update", st.Language, st.Update)
	}

	a.update = &update.Release{Version: "0.2.0", URL: "https://example.com"}
	if err := a.SetLanguage("zh-TW"); err != nil {
		t.Fatal(err)
	}
	st = a.State(ctx)
	if st.Language != "zh-TW" || st.Update == nil || st.Update.Version != "0.2.0" {
		t.Fatalf("state = language %q, update %+v", st.Language, st.Update)
	}
	saved, err := settings.Load()
	if err != nil || saved.Language != "zh-TW" {
		t.Fatalf("saved language = %q, %v; want it persisted", saved.Language, err)
	}

	if err := a.SetLanguage("fr"); err == nil {
		t.Fatal("an unsupported language must be rejected")
	}
}
