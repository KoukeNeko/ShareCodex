package quota

import (
	"math"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
)

func TestRollingUsageAndAbove(t *testing.T) {
	t0 := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	window := 5 * time.Hour

	// One event 2 hours before t0: should be active at t0, expires at t0 + 3h
	e1 := Demand{At: t0.Add(-2 * time.Hour), Weight: 10}
	// One event at t0 + 1h: active until t0 + 6h
	e2 := Demand{At: t0.Add(1 * time.Hour), Weight: 20}
	// One event at t0 + 4h: active until t0 + 9h
	e3 := Demand{At: t0.Add(4 * time.Hour), Weight: 15}

	start := t0
	end := t0.Add(10 * time.Hour)

	r := Rolling([]Demand{e1, e2, e3}, start, end, window)

	// Spans should be:
	// [t0, t0+1h): 10
	// [t0+1h, t0+3h): 10 + 20 = 30 (Peak!)
	// [t0+3h, t0+4h): 20
	// [t0+4h, t0+6h): 20 + 15 = 35 (New Peak!)
	// [t0+6h, t0+9h): 15
	// [t0+9h, t0+10h): 0
	if r.Peak != 35 {
		t.Errorf("got peak %v, want 35", r.Peak)
	}
	if !r.PeakAt.Equal(t0.Add(4 * time.Hour)) {
		t.Errorf("got peak at %v, want %v", r.PeakAt, t0.Add(4*time.Hour))
	}

	// Verify spans cover full start..end
	if len(r.Spans) != 6 {
		t.Fatalf("got %d spans, want 6", len(r.Spans))
	}
	if !r.Spans[0].Start.Equal(start) || !r.Spans[len(r.Spans)-1].End.Equal(end) {
		t.Errorf("spans range %v..%v != %v..%v", r.Spans[0].Start, r.Spans[len(r.Spans)-1].End, start, end)
	}
}

func TestRollingEmptyOrZeroWindow(t *testing.T) {
	t0 := time.Now()
	r := Rolling(nil, t0, t0.Add(time.Hour), 5*time.Hour)
	if r.Peak != 0 || len(r.Spans) != 1 {
		t.Errorf("empty events should have 1 zero-demand span, got %+v", r)
	}

	r0 := Rolling(nil, t0, t0, 5*time.Hour)
	if len(r0.Spans) != 0 {
		t.Errorf("zero span window should have 0 spans")
	}
}

var t0 = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// planFrom is a plan lookup that gives plan from the time from on and the
// reading's own plan before it.
func planFrom(from time.Time, plan string) func(time.Time, string) string {
	return func(t time.Time, snapshot string) string {
		if !t.Before(from) {
			return plan
		}
		return NormalizePlan(snapshot)
	}
}

func TestPlanMultiple(t *testing.T) {
	for plan, want := range map[string]float64{"pro": 1, "Max 5x": 5, " max  20x ": 20} {
		if got, ok := PlanMultiple(plan); !ok || got != want {
			t.Errorf("PlanMultiple(%q) = %v, %v; want %v", plan, got, ok, want)
		}
	}
	if _, ok := PlanMultiple("team"); ok {
		t.Error("team has no published multiple")
	}
	if got := KnownPlans(); len(got) != 3 || got[0] != "pro" || got[2] != "max 20x" {
		t.Errorf("KnownPlans = %v, want smallest first", got)
	}
}

// Readings of one window report reset times that differ by seconds; they are
// one window, and the window's usage sample is its last reading below 100%.
func TestFiveHourWindowsMergeJitterAndSampleLastReadingBelowFull(t *testing.T) {
	resets := t0.Add(5 * time.Hour)
	demand := []Demand{{At: t0.Add(time.Hour), Weight: 40}, {At: t0.Add(2 * time.Hour), Weight: 60}, {At: t0.Add(4 * time.Hour), Weight: 500}}
	readings := []Reading{
		{ObservedAt: t0.Add(30 * time.Minute), ResetsAt: resets.Add(20 * time.Second), UsedPercent: 2},
		{ObservedAt: t0.Add(2*time.Hour + time.Minute), ResetsAt: resets, UsedPercent: 25},
		{ObservedAt: t0.Add(3 * time.Hour), ResetsAt: resets.Add(-30 * time.Second), UsedPercent: 30},
		{ObservedAt: t0.Add(4*time.Hour + time.Minute), ResetsAt: resets, UsedPercent: 100},
		{ObservedAt: t0.Add(4*time.Hour + 6*time.Minute), ResetsAt: resets, UsedPercent: 100},
	}
	ws := FiveHourWindows(readings, planFrom(t0, "max 5x"), demand)
	if len(ws) != 1 {
		t.Fatalf("windows = %+v, want one", ws)
	}
	w := ws[0]
	if !w.Start.Equal(t0) || !w.ResetsAt.Equal(resets) || w.Plan != "max 5x" || w.Excluded != "" {
		t.Fatalf("window = %+v", w)
	}
	// Sample: 30% at 03:00 over the $100 recorded by then.
	if !near(w.Ratio, 0.3) {
		t.Errorf("ratio = %v, want 0.3", w.Ratio)
	}
	if !w.Saturated || !w.FirstFull.Equal(t0.Add(4*time.Hour+time.Minute)) || w.MaxUsed != 100 || !w.MaxAt.Equal(t0.Add(4*time.Hour+time.Minute)) {
		t.Errorf("saturation = %+v", w)
	}
	// Counted once however many readings sat at 100%, and the demand through
	// the first of them.
	if !near(w.CostAtMax, 600) {
		t.Errorf("cost at max = %v, want 600", w.CostAtMax)
	}
}

func TestFiveHourWindowsAreSeparatedByReset(t *testing.T) {
	r := func(observed time.Duration, reset time.Duration, used float64) Reading {
		return Reading{ObservedAt: t0.Add(observed), ResetsAt: t0.Add(reset), UsedPercent: used}
	}
	ws := FiveHourWindows([]Reading{r(time.Hour, 5*time.Hour, 10), r(6*time.Hour, 10*time.Hour, 10), r(2*time.Hour, 5*time.Hour, 20)},
		planFrom(t0, "pro"), []Demand{{At: t0.Add(time.Minute), Weight: 100}, {At: t0.Add(6 * time.Hour), Weight: 100}})
	if len(ws) != 2 || !ws[0].ResetsAt.Equal(t0.Add(5*time.Hour)) || !ws[1].ResetsAt.Equal(t0.Add(10*time.Hour)) {
		t.Fatalf("windows = %+v, want two in order", ws)
	}
	if !near(ws[0].Ratio, 0.2) || !near(ws[1].Ratio, 0.1) {
		t.Errorf("ratios = %v, %v; want 0.2 and 0.1", ws[0].Ratio, ws[1].Ratio)
	}
}

func TestFiveHourWindowExclusions(t *testing.T) {
	resets := t0.Add(5 * time.Hour)
	rd := func(at time.Duration, used float64, plan string) Reading {
		return Reading{ObservedAt: t0.Add(at), ResetsAt: resets, UsedPercent: used, SnapshotPlan: plan}
	}
	demand := []Demand{{At: t0.Add(time.Minute), Weight: MinSampleCost}}
	known := func(plan string) func(time.Time, string) string {
		return func(_ time.Time, snapshot string) string { return NormalizePlan(snapshot) }
	}
	tests := []struct {
		name     string
		readings []Reading
		planAt   func(time.Time, string) string
		demand   []Demand
		want     string
	}{
		{"unknown plan", []Reading{rd(time.Hour, 50, "")}, known(""), demand, ExcludedUnknownPlan},
		{"plan changed inside", []Reading{rd(time.Hour, 50, "pro"), rd(2*time.Hour, 60, "max 5x")}, known(""), demand, ExcludedPlanChanged},
		{"plan known only for part", []Reading{rd(time.Hour, 50, ""), rd(2*time.Hour, 60, "pro")}, known(""), demand, ExcludedPlanChanged},
		{"nothing between 5 and 100", []Reading{rd(time.Hour, 4.9, "pro"), rd(2*time.Hour, 100, "pro")}, known(""), demand, ExcludedNoSample},
		{"too little demand", []Reading{rd(time.Hour, 50, "pro")}, known(""), []Demand{{At: t0.Add(time.Minute), Weight: MinSampleCost - 0.01}}, ExcludedLowCost},
		{"exactly the least demand", []Reading{rd(time.Hour, 50, "pro")}, known(""), demand, ""},
		{"exactly 5 percent", []Reading{rd(time.Hour, 5, "pro")}, known(""), demand, ""},
	}
	for _, tt := range tests {
		ws := FiveHourWindows(tt.readings, tt.planAt, tt.demand)
		if len(ws) != 1 || ws[0].Excluded != tt.want {
			t.Errorf("%s: windows = %+v, want excluded %q", tt.name, ws, tt.want)
			continue
		}
		if tt.want != "" && ws[0].Ratio != 0 {
			t.Errorf("%s: an excluded window gave ratio %v", tt.name, ws[0].Ratio)
		}
		if tt.want == ExcludedPlanChanged && ws[0].Plan != "" {
			t.Errorf("%s: plan = %q, want none", tt.name, ws[0].Plan)
		}
	}
}

// Demand at the window's start and at the sampling reading's instant both
// count.
func TestFiveHourWindowCostBoundsAreInclusive(t *testing.T) {
	resets := t0.Add(5 * time.Hour)
	demand := []Demand{{At: t0.Add(-time.Second), Weight: 1000}, {At: t0, Weight: 4}, {At: t0.Add(time.Hour), Weight: 6}, {At: t0.Add(time.Hour + time.Second), Weight: 1000}}
	ws := FiveHourWindows([]Reading{{ObservedAt: t0.Add(time.Hour), ResetsAt: resets, UsedPercent: 20}}, planFrom(t0, "pro"), demand)
	if len(ws) != 1 || !near(ws[0].Ratio, 2) {
		t.Fatalf("windows = %+v, want ratio 20 / 10", ws)
	}
}

// A plan change restarts a week's use with the reset time unchanged, so the
// week splits at the interval, and a week without an interval is unknown.
func TestWeeklyWindowsStartAtThePlanInterval(t *testing.T) {
	resets := t0.Add(7 * 24 * time.Hour)
	upgrade := t0.Add(3 * 24 * time.Hour)
	intervals := []account.PlanInterval{
		{ID: "a", PlanType: "pro", EffectiveAt: t0.Add(-30 * 24 * time.Hour), EndedAt: &upgrade},
		{ID: "b", PlanType: "Max 5x", EffectiveAt: upgrade},
	}
	demand := []Demand{{At: t0.Add(24 * time.Hour), Weight: 100}, {At: upgrade.Add(time.Hour), Weight: 50}, {At: upgrade.Add(2 * time.Hour), Weight: 50}}
	rd := func(at time.Time, used float64) Reading {
		return Reading{ObservedAt: at, ResetsAt: resets, UsedPercent: used}
	}
	ws := WeeklyWindows([]Reading{
		rd(t0.Add(48*time.Hour), 82),
		rd(upgrade.Add(3*time.Hour), 20),
		rd(upgrade.Add(4*time.Hour), 25),
	}, intervals, demand)
	if len(ws) != 2 {
		t.Fatalf("windows = %+v, want one before and one after the upgrade", ws)
	}
	if ws[0].Plan != "pro" || !ws[0].Start.Equal(resets.Add(-WeeklySpan)) || !near(ws[0].Ratio, 0.82) {
		t.Errorf("before = %+v", ws[0])
	}
	// The after window starts at the interval, not a week before the reset,
	// so the earlier demand does not count against it.
	if ws[1].Plan != "max 5x" || !ws[1].Start.Equal(upgrade) || !near(ws[1].Ratio, 0.25) {
		t.Errorf("after = %+v", ws[1])
	}

	// An interval that began before the week does not move its start.
	long := []account.PlanInterval{{ID: "a", PlanType: "pro", EffectiveAt: t0.Add(-100 * 24 * time.Hour)}}
	ws = WeeklyWindows([]Reading{rd(t0.Add(48*time.Hour), 10)}, long, demand)
	if len(ws) != 1 || !ws[0].Start.Equal(resets.Add(-WeeklySpan)) {
		t.Errorf("window = %+v, want it to start a week before its reset", ws)
	}

	// Without an interval a week's plan is not known, whatever readings say.
	ws = WeeklyWindows([]Reading{{ObservedAt: t0.Add(48 * time.Hour), ResetsAt: resets, UsedPercent: 10, SnapshotPlan: "pro"}}, nil, demand)
	if len(ws) != 1 || ws[0].Excluded != ExcludedUnknownPlan {
		t.Errorf("window = %+v, want unknown plan", ws)
	}
}

func TestCalibrateMedianByPlan(t *testing.T) {
	w := func(plan string, ratio float64, excluded string) Window {
		return Window{Plan: plan, Ratio: ratio, Excluded: excluded}
	}
	cal := Calibrate([]Window{
		w("max 5x", 0.4, ""), w("max 5x", 0.2, ""), w("max 5x", 0.3, ""), w("max 5x", 9, ExcludedLowCost),
		w("max 20x", 0.1, ""), w("max 20x", 0.3, ""),
		{Plan: "pro"},
	})
	if c := cal["max 5x"]; c.Samples != 3 || !near(c.Median, 0.3) || c.Min != 0.2 || c.Max != 0.4 {
		t.Errorf("max 5x = %+v, want an odd count's middle", c)
	}
	if c := cal["max 20x"]; c.Samples != 2 || !near(c.Median, 0.2) {
		t.Errorf("max 20x = %+v, want an even count's mean of the middle two", c)
	}
	if _, ok := cal["pro"]; ok {
		t.Error("a window without a sample calibrated pro")
	}
}

func TestFiveHourRateBasis(t *testing.T) {
	cal := map[string]Calibration{"max 5x": {Samples: 3, Median: 0.25}, "max 20x": {Samples: 1, Median: 0.1}}
	if k, b := FiveHourRate("Max 5x", cal); k != 0.25 || b != BasisMeasured {
		t.Errorf("max 5x = %v, %v; want measured", k, b)
	}
	// One sample is not a measurement: the plan with the most samples, scaled
	// by the published multiple, stands in.
	if k, b := FiveHourRate("max 20x", cal); !near(k, 0.25*5/20) || b != BasisDerived {
		t.Errorf("max 20x = %v, %v; want derived from max 5x", k, b)
	}
	if k, b := FiveHourRate("pro", cal); !near(k, 1.25) || b != BasisDerived {
		t.Errorf("pro = %v, %v; want derived from max 5x", k, b)
	}
	if _, b := FiveHourRate("max 5x", nil); b != BasisInsufficient {
		t.Errorf("without samples basis = %v, want insufficient", b)
	}
	if _, b := FiveHourRate("team", cal); b != BasisInsufficient {
		t.Errorf("a plan without a published multiple, basis = %v, want insufficient", b)
	}
	// A plan's single sample can stand for itself, but only as derived.
	if k, b := FiveHourRate("max 20x", map[string]Calibration{"max 20x": {Samples: 1, Median: 0.1}}); k != 0.1 || b != BasisDerived {
		t.Errorf("a lone sample = %v, %v; want derived 0.1", k, b)
	}
	// Samples decide among sources; a tie goes to the smaller name.
	tie := map[string]Calibration{"pro": {Samples: 1, Median: 1.5}, "max 5x": {Samples: 1, Median: 0.3}}
	if k, _ := FiveHourRate("max 20x", tie); !near(k, 0.3*5/20) {
		t.Errorf("tie = %v, want from max 5x", k)
	}
}

// No weekly multiple is published, so a weekly rate is never derived.
func TestWeeklyRateIsNeverDerived(t *testing.T) {
	cal := map[string]Calibration{"max 5x": {Samples: 1, Median: 0.042}}
	if k, b := WeeklyRate("max 5x", cal); k != 0.042 || b != BasisMeasured {
		t.Errorf("max 5x = %v, %v; want measured from one sample", k, b)
	}
	if _, b := WeeklyRate("max 20x", cal); b != BasisInsufficient {
		t.Errorf("max 20x basis = %v, want insufficient", b)
	}
}

func TestFit(t *testing.T) {
	for peak, want := range map[float64]string{0: FitSafe, 79.99: FitSafe, 80: FitBorderline, 100: FitBorderline, 100.01: FitOver} {
		if got := Fit(peak); got != want {
			t.Errorf("Fit(%v) = %q, want %q", peak, got, want)
		}
	}
}

func hours(h float64) time.Time { return t0.Add(time.Duration(h * float64(time.Hour))) }

func TestReplaySessionsOpenAtTheFirstRequestAfterTheLastEnds(t *testing.T) {
	demand := []Demand{
		{At: hours(0), Weight: 60},
		{At: hours(4.9), Weight: 30}, // same session: 90 of 100
		{At: hours(5), Weight: 60},   // exactly at the end: a new session
		{At: hours(5), Weight: 60},   // simultaneous: the same session, now 120
		{At: hours(5.5), Weight: 10}, // after the hit: blocked
		{At: hours(20), Weight: 100}, // fills the session exactly: not a hit
	}
	r := ReplaySessions(demand, 100, FiveHourSpan, hours(0), hours(24))
	if r.Windows != 3 || r.HitWindows != 1 {
		t.Fatalf("windows = %d, hits = %d; want 3 and 1", r.Windows, r.HitWindows)
	}
	// The request that crosses the line is the hit; only later ones are blocked.
	if !r.FirstHit.Equal(hours(5)) || !near(r.Blocked, 10) || !near(r.Total, 320) {
		t.Errorf("replay = %+v", r)
	}
	if !near(r.PeakPercent, 130) || !near(r.BlockedPercent(), 10.0/320*100) {
		t.Errorf("peak = %v, blocked = %v", r.PeakPercent, r.BlockedPercent())
	}
}

func TestReplaySessionsCountOnlyWindowsOpenedInTheSpan(t *testing.T) {
	demand := []Demand{{At: hours(0), Weight: 500}, {At: hours(6), Weight: 10}, {At: hours(30), Weight: 10}}
	// The session at hour 0 only places the one at 6; the one at 30 is after the span.
	r := ReplaySessions(demand, 100, FiveHourSpan, hours(1), hours(24))
	if r.Windows != 1 || r.HitWindows != 0 || !near(r.Total, 10) || !near(r.PeakPercent, 10) {
		t.Errorf("replay = %+v, want only the session opened at hour 6", r)
	}
	// Zero-weight requests and a missing capacity replay nothing.
	if r := ReplaySessions([]Demand{{At: hours(0), Weight: 0}}, 100, FiveHourSpan, hours(0), hours(1)); r.Windows != 0 {
		t.Errorf("a weightless request opened a session: %+v", r)
	}
	if r := ReplaySessions(demand, 0, FiveHourSpan, hours(0), hours(24)); r.Windows != 0 {
		t.Errorf("zero capacity replayed %+v", r)
	}
}

// Everyone on one limit hits it where two separate limits do not, and the
// separate replays add up.
func TestReplaySessionsSharedAndSeparate(t *testing.T) {
	alice := []Demand{{At: hours(0), Weight: 70}}
	bob := []Demand{{At: hours(10), Weight: 70}}
	shared := ReplaySessions(append(slicesClone(alice), bob...), 100, FiveHourSpan, hours(0), hours(24))
	if shared.HitWindows != 0 || shared.Windows != 2 {
		t.Errorf("shared with disjoint peaks = %+v, want two sessions and no hit", shared)
	}
	overlap := []Demand{{At: hours(1), Weight: 70}}
	shared = ReplaySessions(sorted(append(slicesClone(alice), overlap...)), 100, FiveHourSpan, hours(0), hours(24))
	if shared.HitWindows != 1 || !near(shared.PeakPercent, 140) {
		t.Errorf("shared with overlapping peaks = %+v, want a hit", shared)
	}
	separate := ReplaySessions(alice, 100, FiveHourSpan, hours(0), hours(24)).Merge(ReplaySessions(overlap, 100, FiveHourSpan, hours(0), hours(24)))
	if separate.HitWindows != 0 || separate.Windows != 2 || !near(separate.PeakPercent, 70) || !near(separate.Total, 140) {
		t.Errorf("separate = %+v, want two sessions and no hit", separate)
	}
	a := SessionReplay{HitWindows: 1, FirstHit: hours(9)}
	b := SessionReplay{HitWindows: 1, FirstHit: hours(3)}
	if m := (SessionReplay{}).Merge(a).Merge(b); !m.FirstHit.Equal(hours(3)) || m.HitWindows != 2 {
		t.Errorf("merged = %+v, want the earliest first hit", m)
	}
}

func slicesClone(d []Demand) []Demand { return append([]Demand(nil), d...) }

func sorted(d []Demand) []Demand {
	for i := range d {
		for j := i + 1; j < len(d); j++ {
			if d[j].At.Before(d[i].At) {
				d[i], d[j] = d[j], d[i]
			}
		}
	}
	return d
}

func TestReplayWeeklyStepsBackFromTheAnchor(t *testing.T) {
	week := WeeklySpan
	anchor := t0.Add(2*week + 36*time.Hour) // the latest reset, in the future
	demand := []Demand{
		{At: anchor.Add(-week - 24*time.Hour), Weight: 40}, // two weeks back
		{At: anchor.Add(-6 * 24 * time.Hour), Weight: 70},
		{At: anchor.Add(-6*24*time.Hour + time.Hour), Weight: 40}, // passes 100
		{At: anchor.Add(-time.Hour), Weight: 10},
	}
	from, to := anchor.Add(-week-time.Hour), anchor.Add(-time.Minute)
	ws := ReplayWeekly(demand, 100, anchor, from, to)
	if len(ws) != 2 {
		t.Fatalf("weeks = %+v, want the two overlapping the span", ws)
	}
	if !ws[0].End.Equal(anchor.Add(-week)) || !near(ws[0].PeakPercent, 40) || !ws[0].HitAt.IsZero() {
		t.Errorf("first week = %+v", ws[0])
	}
	if !ws[1].End.Equal(anchor) || !near(ws[1].PeakPercent, 120) || !ws[1].HitAt.Equal(anchor.Add(-6*24*time.Hour+time.Hour)) {
		t.Errorf("second week = %+v", ws[1])
	}

	// A span inside one week, with the anchor in the past, is that week alone.
	ws = ReplayWeekly(demand, 100, anchor.Add(-10*week), anchor.Add(-3*24*time.Hour), anchor)
	if len(ws) != 1 || !ws[0].End.Equal(anchor) {
		t.Errorf("weeks = %+v, want the week ending at the anchor", ws)
	}
	// A reset exactly at the span's start belongs to the week before it.
	ws = ReplayWeekly(demand, 100, anchor, anchor.Add(-week), anchor.Add(-week+time.Hour))
	if len(ws) != 1 || !ws[0].Start.Equal(anchor.Add(-week)) {
		t.Errorf("weeks = %+v, want the week that begins at the span's start", ws)
	}
	if ReplayWeekly(demand, 0, anchor, from, to) != nil {
		t.Error("zero capacity replayed weeks")
	}
}

func TestWorstWeekly(t *testing.T) {
	a := []WeeklyReplay{{PeakPercent: 50, HitAt: hours(9)}, {PeakPercent: 120, HitAt: hours(4)}}
	b := []WeeklyReplay{{PeakPercent: 110, HitAt: hours(2)}, {PeakPercent: 30}}
	w := WorstWeekly(a, b)
	if w[0].PeakPercent != 110 || !w[0].HitAt.Equal(hours(2)) || w[1].PeakPercent != 120 || !w[1].HitAt.Equal(hours(4)) {
		t.Errorf("worst = %+v", w)
	}
	if a[0].PeakPercent != 50 {
		t.Error("WorstWeekly changed its input")
	}
}
