package query_test

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/attribution"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/server/query"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage/storagetest"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

const demandModel = "claude-opus-5-5"

// capacityBase is the start of the seeded account's history; offsets below
// are hours from it.
var capacityBase = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func at(hours float64) time.Time {
	return capacityBase.Add(time.Duration(hours * float64(time.Hour)))
}

func near(got, want, tolerance float64) bool { return math.Abs(got-want) <= tolerance }

func pair(t *testing.T, store *storage.Store, name string) storage.Device {
	t.Helper()
	ctx := context.Background()
	person, err := store.AddPerson(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := store.CreateInvite(ctx, person.ID, storage.InviteTTL)
	if err != nil {
		t.Fatal(err)
	}
	device, _, err := store.Pair(ctx, code, name+"-laptop", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	return device
}

type usedReading struct {
	at      float64
	resets  float64
	used    float64
	plan    string
	source  string
	weekly  bool
	started bool
}

// seedCapacity records the history the tests read:
//
//	hours   -6..  unknown plan: no interval, readings carry no plan
//	0..24         pro interval            5h samples 0.50, 0.54
//	24..48        max 5x interval         5h samples 0.25, 0.26; one window at 100% in three readings
//	48..72        max 20x interval        5h samples 0.11, 0.10; the window across hour 48 spans the change
//	after 72      no interval; readings carry "Max 20x"   5h sample 0.11, no weekly sample
//
// Every request costs the amount noted in demand; alice and bob never
// overlap except in the hour-75 session.
func seedCapacity(t *testing.T) (*storage.Store, string, time.Time) {
	t.Helper()
	ctx := context.Background()
	store := storagetest.New(t)
	alice, bob := pair(t, store, "alice"), pair(t, store, "bob")

	unit := attribution.Cost(demandModel, usage.Tokens{Output: 1_000_000})
	event := func(key string, hours, usd float64) syncapi.Event {
		return syncapi.Event{DedupeKey: key, AccountRefHash: "acct", Provider: "anthropic", Product: "claude-code", Model: demandModel,
			OccurredAt: at(hours), Output: int64(usd / unit * 1_000_000)}
	}
	aliceEvents := []syncapi.Event{
		event("a0", -5, 100), event("a1", 2, 100), event("a2", 9, 50), event("a3", 27, 200), event("a4", 46, 50),
		event("a5", 56, 300), event("a6", 75, 250), event("a7", 81, 100),
	}
	bobEvents := []syncapi.Event{event("b0", 34, 100), event("b1", 36.5, 400), event("b2", 66, 300), event("b3", 76, 250)}
	// A request to a third-party model carries no demand.
	bobEvents = append(bobEvents, syncapi.Event{DedupeKey: "tp", AccountRefHash: "acct", Provider: "anthropic", Product: "claude-code",
		Model: "ocx-claude-native--gpt-6-sol", Gateway: "opencodex", ThirdParty: true, OccurredAt: at(34), Output: 5_000_000})

	five, week := 300, 10080
	weekResets := at(168)
	var snapshots []syncapi.Snapshot
	reading := func(r usedReading) {
		resets, minutes, key := at(r.resets), five, "five_hour"
		if r.weekly {
			resets, minutes, key = weekResets, week, "weekly"
		}
		source := r.source
		if source == "" {
			source = string(quota.SourceClaudeOAuthUsage)
		}
		used := r.used
		snapshots = append(snapshots, syncapi.Snapshot{Provider: "anthropic", AccountRefHash: "acct", Source: source,
			ObservedAt: at(r.at), PlanType: r.plan,
			Buckets: []syncapi.Bucket{{Key: key, UsedPercent: &used, ResetsAt: &resets, WindowMinutes: &minutes}}})
	}
	for _, r := range []usedReading{
		// Unknown plan.
		{at: -3, resets: -1, used: 40},
		// Pro.
		{at: 3, resets: 6, used: 50}, {at: 11, resets: 13, used: 27},
		// Max 5x.
		{at: 29, resets: 31, used: 50}, {at: 36, resets: 38, used: 26},
		{at: 37, resets: 38, used: 100}, {at: 37.05, resets: 38, used: 100}, {at: 37.1, resets: 38, used: 100},
		// Across the change to Max 20x.
		{at: 47, resets: 50, used: 30}, {at: 49, resets: 50, used: 10},
		// Max 20x: a statusLine figure of the window is ignored, and so is a
		// reading of a window that has not started.
		{at: 54, resets: 59, used: 0},
		{at: 58, resets: 60, used: 33}, {at: 58.5, resets: 60, used: 99, source: string(quota.SourceClaudeStatusLine)},
		{at: 68, resets: 70, used: 30},
		// No interval: the reading's own plan, in the provider's case.
		{at: 83, resets: 85, used: 11, plan: "Max 20x"},
		// Weeks, at instants of their own: a device stores one reading per source and instant.
		{at: 20, used: 30, weekly: true}, {at: 47.2, used: 40, weekly: true},
		{at: 60, used: 20, weekly: true}, {at: 71, used: 40, weekly: true}, {at: 83.2, used: 50, weekly: true, plan: "Max 20x"},
	} {
		reading(r)
	}

	if _, err := store.Ingest(ctx, alice, syncapi.SyncRequest{Version: syncapi.Version, Events: aliceEvents, Snapshots: snapshots}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Ingest(ctx, bob, syncapi.SyncRequest{Version: syncapi.Version, Events: bobEvents}); err != nil {
		t.Fatal(err)
	}
	accounts, err := store.Accounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %v, %v", accounts, err)
	}
	id := accounts[0].ID

	end := func(hours float64) *time.Time { t := at(hours); return &t }
	for _, in := range []account.PlanInterval{
		{PlanType: "pro", EffectiveAt: at(0), EndedAt: end(24)},
		{PlanType: "max 5x", EffectiveAt: at(24), EndedAt: end(48)},
		{PlanType: "Max 20x", EffectiveAt: at(48), EndedAt: end(72)},
	} {
		in.AccountID, in.Source = id, "admin"
		if _, err := store.SavePlanInterval(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	return store, id, at(90)
}

func TestAccountCapacityCalibratesPlansFromTheirOwnWindows(t *testing.T) {
	store, id, now := seedCapacity(t)
	rep, err := query.AccountCapacity(context.Background(), store, id, query.PeriodByID("7d"), now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reason != "" || rep.Provider != "anthropic" {
		t.Fatalf("report = %+v", rep)
	}

	rows := map[string]syncapi.CalibrationRow{}
	for _, r := range rep.Calibration {
		rows[r.Bucket+"/"+r.Plan] = r
	}
	// Each plan's median over its own windows: the unknown-plan window, the
	// one spanning the change, the statusLine figure and the window that had
	// not started give none.
	for key, want := range map[string]struct {
		n    int
		rate float64
	}{
		"five_hour/pro": {2, 0.52}, "five_hour/max 5x": {2, 0.255}, "five_hour/max 20x": {3, 0.11},
	} {
		got, ok := rows[key]
		if !ok || got.Samples != want.n || !near(got.Rate, want.rate, 0.002) || !near(got.CapacityUSD, 100/got.Rate, 1e-9) {
			t.Errorf("%s = %+v, want %d samples at %v", key, got, want.n, want.rate)
		}
	}
	if _, ok := rows["five_hour/"]; ok || len(rep.Calibration) != 3+3 {
		t.Errorf("calibration = %+v, want the three plans of both limits", rep.Calibration)
	}
	// A week starts no earlier than its plan interval: Max 5x used 40% of its
	// week over the $750 recorded from hour 24, not over the week's $900; a
	// reading with no interval gives no week at all.
	for key, want := range map[string]float64{"weekly/pro": 0.2, "weekly/max 5x": 40.0 / 750, "weekly/max 20x": 40.0 / 600} {
		if got := rows[key]; got.Samples != 1 || !near(got.Rate, want, 0.0005) {
			t.Errorf("%s = %+v, want one sample at %v", key, got, want)
		}
	}

	var fiveHour *syncapi.CapacityMultiple
	for i, m := range rep.Multiples {
		if m.Bucket == "five_hour" {
			fiveHour = &rep.Multiples[i]
		}
	}
	if fiveHour == nil || fiveHour.Official != 4 || !near(fiveHour.Observed, 0.255/0.11, 0.02) {
		t.Errorf("multiples = %+v, want an observed 5-hour multiple beside the official 4", rep.Multiples)
	}

	windows := map[time.Time]syncapi.ObservedWindow{}
	for _, w := range rep.Windows {
		windows[w.Start] = w
	}
	if len(rep.Windows) != 9 {
		t.Errorf("windows = %d, want 9 (the not-started reading makes none)", len(rep.Windows))
	}
	if w := windows[at(-6)]; w.Excluded != quota.ExcludedUnknownPlan || w.Plan != "" {
		t.Errorf("window without a plan = %+v", w)
	}
	if w := windows[at(45)]; w.Excluded != quota.ExcludedPlanChanged || w.Plan != "" {
		t.Errorf("window across the change = %+v", w)
	}
	w := windows[at(33)]
	if !w.Saturated || w.FirstFull == nil || !w.FirstFull.Equal(at(37)) || w.Plan != "max 5x" || w.Excluded != "" {
		t.Errorf("saturated window = %+v", w)
	}
	if w := windows[at(26)]; w.PredictedPercent == nil || !near(*w.PredictedPercent, 51, 0.5) || !near(w.MaxPercent, 50, 1e-9) {
		t.Errorf("window to check against = %+v, want 50%% against about 51%% predicted", w)
	}
	if w := windows[at(80)]; w.Plan != "max 20x" || w.Excluded != "" {
		t.Errorf("a window planned by its own readings = %+v", w)
	}

	// Windows, not readings: three readings at 100% are one window.
	sat := map[string]syncapi.SaturationRow{}
	for _, s := range rep.Saturation {
		sat[s.Bucket+"/"+s.Plan] = s
	}
	if s := sat["five_hour/max 5x"]; s.Windows != 2 || s.Saturated != 1 {
		t.Errorf("max 5x saturation = %+v, want 1 of 2 windows", s)
	}
	if s := sat["five_hour/"]; s.Windows != 2 {
		t.Errorf("unknown plan saturation = %+v, want the two unplanned windows", s)
	}
}

func TestAccountCapacityReplaysSharedAndSeparatePlans(t *testing.T) {
	store, id, now := seedCapacity(t)
	rep, err := query.AccountCapacity(context.Background(), store, id, query.PeriodByID("7d"), now)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := map[string]syncapi.CapacityScenario{}
	for _, s := range rep.Scenarios {
		scenarios[s.ID] = s
	}
	if len(rep.Scenarios) != 3 {
		t.Fatalf("scenarios = %+v", rep.Scenarios)
	}

	// Max 5x: $392 a session. Bob alone passes it at hour 36.5, and alice and
	// bob together at hour 76; separately only bob does.
	shared, separate := scenarios["shared_max5"], scenarios["separate_max5"]
	if shared.FiveHour.Basis != "measured" || !near(shared.FiveHour.CapacityUSD, 100/0.255, 1) {
		t.Errorf("shared max 5x = %+v", shared.FiveHour)
	}
	if shared.FiveHour.HitWindows != 2 || shared.FiveHour.FirstHit == nil || !shared.FiveHour.FirstHit.Equal(at(36.5)) || shared.FiveHour.Fit != quota.FitOver {
		t.Errorf("shared max 5x = %+v, want hits at hours 36.5 and 76", shared.FiveHour)
	}
	if separate.FiveHour.HitWindows != 1 || len(separate.Members) != 2 {
		t.Fatalf("separate max 5x = %+v", separate)
	}
	for _, m := range separate.Members {
		want := 0
		if m.Name == "bob" {
			want = 1
		}
		if m.FiveHour.HitWindows != want {
			t.Errorf("separate %s hits = %d, want %d", m.Name, m.FiveHour.HitWindows, want)
		}
	}
	if separate.Shared || !shared.Shared {
		t.Error("shared flags are swapped")
	}

	// Max 20x is $909 a session: nothing passes it, and its weekly limit is
	// measured from one sample.
	max20 := scenarios["shared_max20"]
	if max20.FiveHour.HitWindows != 0 || max20.FiveHour.Fit != quota.FitSafe || max20.Weekly.Basis != "measured" || len(max20.Weekly.Windows) != 2 {
		t.Errorf("shared max 20x = %+v", max20)
	}
	if !near(max20.Weekly.CapacityUSD, 600/0.4, 1) || max20.Weekly.HitWindows != 1 {
		t.Errorf("max 20x weekly = %+v, want $1,500 and the week of $2,100 over it", max20.Weekly)
	}
	// Demand outside the quota's own models is no demand: bob's third-party request.
	if !near(rep.Demand.CostUSD, 2200, 5) {
		t.Errorf("demand = %+v", rep.Demand)
	}

	var names []string
	for _, m := range rep.Members {
		names = append(names, m.Name)
	}
	if len(names) != 2 || rep.Members[0].CostUSD < rep.Members[1].CostUSD {
		t.Errorf("members = %+v, want both, most demand first", rep.Members)
	}
}

// Without samples a plan's rate is derived from another's by the published
// multiple for 5 hours, and a week is never derived.
func TestAccountCapacityDerivesOnlyTheFiveHourLimit(t *testing.T) {
	ctx := context.Background()
	store := storagetest.New(t)
	device := pair(t, store, "alice")
	unit := attribution.Cost(demandModel, usage.Tokens{Output: 1_000_000})
	five := 300
	now := at(100)
	used := 40.0
	resets := at(100).Add(-time.Hour)
	_, err := store.Ingest(ctx, device, syncapi.SyncRequest{Version: syncapi.Version,
		Events: []syncapi.Event{{DedupeKey: "e", AccountRefHash: "acct", Provider: "anthropic", Product: "claude-code", Model: demandModel,
			OccurredAt: now.Add(-3 * time.Hour), Output: int64(100 / unit * 1_000_000)}},
		Snapshots: []syncapi.Snapshot{{Provider: "anthropic", AccountRefHash: "acct", Source: "claude-oauth-usage", PlanType: "pro",
			ObservedAt: now.Add(-2 * time.Hour), Buckets: []syncapi.Bucket{{Key: "five_hour", UsedPercent: &used, ResetsAt: &resets, WindowMinutes: &five}}}}})
	if err != nil {
		t.Fatal(err)
	}
	accounts, _ := store.Accounts(ctx)
	rep, err := query.AccountCapacity(ctx, store, accounts[0].ID, query.PeriodByID("24h"), now)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]syncapi.CapacityScenario{}
	for _, s := range rep.Scenarios {
		by[s.ID] = s
	}
	// One Pro sample of 0.4 %/USD: a Max 5x window holds five times the work.
	if got := by["shared_max5"].FiveHour; got.Basis != "derived" || !near(got.CapacityUSD, 100/(0.4/5), 1) {
		t.Errorf("shared max 5x 5h = %+v, want derived from pro", got)
	}
	if got := by["shared_max5"].Weekly; got.Basis != "insufficient" || got.CapacityUSD != 0 || len(got.Windows) != 0 {
		t.Errorf("shared max 5x weekly = %+v, want insufficient", got)
	}
}

func TestAccountCapacityOfAnotherProviderHasNoScenarios(t *testing.T) {
	ctx := context.Background()
	store := storagetest.New(t)
	device := pair(t, store, "alice")
	_, err := store.Ingest(ctx, device, syncapi.SyncRequest{Version: syncapi.Version,
		Events: []syncapi.Event{{DedupeKey: "e", AccountRefHash: "codex", Provider: "openai", Product: "codex", Model: "gpt-6-sol",
			OccurredAt: at(1), Input: 1000, Output: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	accounts, _ := store.Accounts(ctx)
	rep, err := query.AccountCapacity(ctx, store, accounts[0].ID, query.PeriodByID("30d"), at(2))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reason != syncapi.CapacityReasonUnsupportedProvider || len(rep.Scenarios) != 0 || len(rep.Calibration) != 0 {
		t.Errorf("report = %+v, want a reason and no scenarios", rep)
	}
	if rep.Demand.Requests != 1 || len(rep.Members) != 1 || rep.Members[0].Name != "alice" {
		t.Errorf("demand and members = %+v, %+v, want alice's request", rep.Demand, rep.Members)
	}
}

func TestAccountCapacityListsLoggedLimitsAndCountsConfirmedOnes(t *testing.T) {
	ctx := context.Background()
	store, id, now := seedCapacity(t)
	carol := pair(t, store, "carol")
	hit := func(key string, hours float64, kind usage.LimitKind, evidence string) syncapi.LimitEvent {
		return syncapi.LimitEvent{DedupeKey: key, AccountRefHash: "acct", Provider: "anthropic", OccurredAt: at(hours), ObservedAt: at(hours),
			SessionID: "secret-session", RequestID: "secret-request", Kind: string(kind), Source: "claude-transcript", Evidence: evidence}
	}
	req := syncapi.SyncRequest{Version: syncapi.Version, LimitEvents: []syncapi.LimitEvent{
		hit("a", 37, usage.LimitFiveHour, "five_hour"), hit("b", 40, usage.LimitProvider429, "gateway"), hit("c", 60, usage.LimitWeekly, "seven_day"),
	}}
	if _, err := store.Ingest(ctx, carol, req); err != nil {
		t.Fatal(err)
	}
	rep, err := query.AccountCapacity(ctx, store, id, query.PeriodByID("7d"), now)
	if err != nil {
		t.Fatal(err)
	}

	if len(rep.LimitHits) != 3 {
		t.Fatalf("limit hits = %+v, want three, newest first", rep.LimitHits)
	}
	for i, want := range []struct{ kind, evidence, plan string }{
		{"weekly", "seven_day", "max 20x"}, {"provider429", "gateway", "max 5x"}, {"5h", "five_hour", "max 5x"},
	} {
		h := rep.LimitHits[i]
		if h.Kind != want.kind || h.Evidence != want.evidence || h.Plan != want.plan || h.Member != "carol" {
			t.Errorf("hit %d = %+v, want %+v by carol", i, h, want)
		}
	}
	// The gateway throttle is listed but is no plan limit.
	sat := map[string]syncapi.SaturationRow{}
	for _, s := range rep.Saturation {
		sat[s.Bucket+"/"+s.Plan] = s
	}
	if sat["five_hour/max 5x"].Hits != 1 || sat["weekly/max 20x"].Hits != 1 || sat["five_hour/max 20x"].Hits != 0 {
		t.Errorf("hits per plan = %+v", sat)
	}
	marked := 0
	for _, w := range rep.Windows {
		if w.LimitHit {
			marked++
			if !w.Start.Equal(at(33)) {
				t.Errorf("window %v is marked, want the one holding the 5-hour limit", w.Start)
			}
		}
	}
	if marked != 1 {
		t.Errorf("%d windows marked, want 1", marked)
	}
	raw, err := json.Marshal(rep.LimitHits)
	if err != nil || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "laptop") {
		t.Errorf("limit hits %s carry a session, request, or device", raw)
	}
}
