package identity

import (
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
)

func TestParse(t *testing.T) {
	now := time.Unix(100, 0)
	tests := []struct {
		name       string
		out        string
		wantPooled bool
		wantHint   string
	}{
		{"subscription", `{"loggedIn":true,"authMethod":"claude.ai","email":"someone@example.com","orgId":"org-1","subscriptionType":"max"}`, true, "so***@example.com"},
		{"api key", `{"loggedIn":true,"authMethod":"api_key","email":"","orgId":"org-1"}`, false, ""},
		{"signed out", `{"loggedIn":false}`, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := parse([]byte(tt.out), now)
			if err != nil {
				t.Fatal(err)
			}
			if (o.ExternalRefHash != "") != tt.wantPooled || o.Hint != tt.wantHint {
				t.Errorf("got %+v", o)
			}
			if tt.wantPooled && o.ExternalRefHash != account.HashExternalRef(account.ProviderAnthropic, "org-1") {
				t.Error("org ID must be preferred over email as the account reference")
			}
		})
	}
}
