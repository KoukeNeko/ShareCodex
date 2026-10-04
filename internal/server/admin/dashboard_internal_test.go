package admin

import (
	"testing"

	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// The report is shared through the cache, so redacting it must leave the
// cached copy as it was.
func TestPublicCapacityLeavesTheCachedReportAlone(t *testing.T) {
	cached := syncapi.CapacityReport{
		AccountLabel: "al***@example.com",
		Plans:        []syncapi.PlanIntervalDTO{{PlanType: "max 5x", Reason: "private", Source: "admin"}},
	}
	got := publicCapacity(cached, "Account 1")
	if got.AccountLabel != "Account 1" || got.Plans[0].Reason != "" || got.Plans[0].Source != "" {
		t.Errorf("redacted report = %+v", got)
	}
	if cached.AccountLabel != "al***@example.com" || cached.Plans[0].Reason != "private" || cached.Plans[0].Source != "admin" {
		t.Errorf("cached report changed: %+v", cached)
	}
}
