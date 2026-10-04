package query

import (
	"context"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/attribution"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// capacityLookback is how far back readings and demand calibrate a plan's
// limits. A week more of demand covers the window a reading from its start
// began in.
const capacityLookback = 90 * 24 * time.Hour

// capacityScenarios are the plans the recorded demand is replayed under: one
// plan for everyone, one for each member, and a larger one for everyone.
var capacityScenarios = []struct {
	id     string
	plan   string
	shared bool
}{
	{"shared_max5", "max 5x", true},
	{"separate_max5", "max 5x", false},
	{"shared_max20", "max 20x", true},
}

// AccountCapacity measures an account's plan limits against its recorded
// demand and replays that demand under other plans, over a period. Demand is
// each official request's API-equivalent cost, a weight and not a bill.
func AccountCapacity(ctx context.Context, st *storage.Store, accountID string, period Period, now time.Time) (syncapi.CapacityReport, error) {
	acc, err := st.Account(ctx, accountID)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}
	members, err := st.Members(ctx, accountID)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}
	persons, err := st.Persons(ctx)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}
	plans, err := st.PlanHistory(ctx, accountID)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}
	history := now.Add(-capacityLookback)
	events, err := st.AccountDemandEvents(ctx, accountID, history.Add(-quota.WeeklySpan), now)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}

	start, end := now.Add(-period.Span()), now
	report := syncapi.CapacityReport{
		AccountID: acc.ID, AccountLabel: acc.Label, Provider: string(acc.Provider), CurrentPlan: acc.PlanType,
		Period: period.ID, Start: start.UTC(), End: end.UTC(),
		Members: []syncapi.CapacityMember{}, Plans: []syncapi.PlanIntervalDTO{}, Calibration: []syncapi.CalibrationRow{},
		Multiples: []syncapi.CapacityMultiple{}, Windows: []syncapi.ObservedWindow{}, Saturation: []syncapi.SaturationRow{},
		Scenarios: []syncapi.CapacityScenario{},
	}
	for _, p := range plans {
		report.Plans = append(report.Plans, syncapi.PlanIntervalDTO{ID: p.ID, PlanType: p.PlanType, EffectiveAt: p.EffectiveAt,
			EndedAt: p.EndedAt, Reason: p.Reason, Source: p.Source, Precision: p.Precision})
	}

	d := newDemand(events, start, end)
	rolling := quota.Rolling(d.all, start, end, quota.FiveHourSpan)
	report.Demand = syncapi.CapacityDemand{Tokens: d.tokens, Requests: d.requests, CostUSD: d.cost,
		RollingPeak: rolling.Peak, RollingPeakAt: rolling.PeakAt}
	memberRolling := map[string]quota.RollingUsage{}
	names := map[string]string{}
	for _, p := range persons {
		names[p.ID] = p.DisplayName
	}
	// Members are listed even without demand; so is anyone who used the
	// account in the period and has since left it.
	personIDs := make([]string, 0, len(members))
	for _, m := range members {
		personIDs = append(personIDs, m.PersonID)
	}
	for id := range d.personRequests {
		if !slices.Contains(personIDs, id) {
			personIDs = append(personIDs, id)
		}
	}
	sort.Slice(personIDs, func(i, j int) bool { return names[personIDs[i]] < names[personIDs[j]] })
	for _, id := range personIDs {
		memberRolling[id] = quota.Rolling(d.byPerson[id], start, end, quota.FiveHourSpan)
		report.Members = append(report.Members, syncapi.CapacityMember{PersonID: id, Name: names[id],
			Tokens: d.personTokens[id], Requests: d.personRequests[id], CostUSD: d.personCost[id], RollingPeak: memberRolling[id].Peak})
	}
	sort.SliceStable(report.Members, func(i, j int) bool { return report.Members[i].CostUSD > report.Members[j].CostUSD })

	if acc.Provider != account.ProviderAnthropic {
		report.Reason = syncapi.CapacityReasonUnsupportedProvider
		return report, nil
	}

	snaps, err := st.HistoricalSnapshots(ctx, accountID, history, now)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}
	planAt := func(t time.Time, snapshotPlan string) string {
		if p := account.ResolvePlanAt(plans, t); p != "" {
			return quota.NormalizePlan(p)
		}
		return quota.NormalizePlan(snapshotPlan)
	}
	fiveHourReadings := readings(snaps, quota.BucketFiveHour, quota.FiveHourSpan)
	weeklyReadings := readings(snaps, quota.BucketWeekly, quota.WeeklySpan)
	fiveHour := quota.FiveHourWindows(fiveHourReadings, planAt, d.all)
	weekly := quota.WeeklyWindows(weeklyReadings, plans, d.all)
	fiveHourCal, weeklyCal := quota.Calibrate(fiveHour), quota.Calibrate(weekly)

	report.Calibration = append(calibrationRows(quota.BucketFiveHour, fiveHourCal), calibrationRows(quota.BucketWeekly, weeklyCal)...)
	report.Multiples = multiples(fiveHourCal, weeklyCal)
	report.Windows = observedWindows(fiveHour, fiveHourCal, start, end)
	report.Saturation = append(saturationRows(quota.BucketFiveHour, fiveHour, start, end), saturationRows(quota.BucketWeekly, weekly, start, end)...)

	var anchor time.Time
	if n := len(weeklyReadings); n > 0 {
		latest := slices.MaxFunc(weeklyReadings, func(a, b quota.Reading) int { return a.ObservedAt.Compare(b.ObservedAt) })
		anchor = latest.ResetsAt
	}
	for _, sc := range capacityScenarios {
		scenario := syncapi.CapacityScenario{ID: sc.id, Plan: sc.plan, Shared: sc.shared}
		fiveRate, fiveBasis := quota.FiveHourRate(sc.plan, fiveHourCal)
		weekRate, weekBasis := quota.WeeklyRate(sc.plan, weeklyCal)
		replay := func(demand []quota.Demand, rolling quota.RollingUsage) (fiveHourPart, []quota.WeeklyReplay) {
			var part fiveHourPart
			var weeks []quota.WeeklyReplay
			if fiveBasis != quota.BasisInsufficient {
				// A rate is percent per USD, so it also turns the rolling
				// demand into percent of the limit.
				part = fiveHourPart{replay: quota.ReplaySessions(demand, 100/fiveRate, quota.FiveHourSpan, start, end),
					rollingPeak: rolling.Peak * fiveRate, rollingP50: rolling.P50 * fiveRate, rollingP95: rolling.P95 * fiveRate}
			}
			if weekBasis != quota.BasisInsufficient {
				weeks = quota.ReplayWeekly(demand, 100/weekRate, anchor, start, end)
			}
			return part, weeks
		}
		if sc.shared {
			part, weeks := replay(d.all, rolling)
			scenario.FiveHour = part.result(fiveBasis, fiveRate)
			scenario.Weekly = weeklyResult(weekBasis, weekRate, weeks)
		} else {
			var total fiveHourPart
			var allWeeks [][]quota.WeeklyReplay
			for _, id := range personIDs {
				part, weeks := replay(d.byPerson[id], memberRolling[id])
				total = total.merge(part)
				allWeeks = append(allWeeks, weeks)
				scenario.Members = append(scenario.Members, syncapi.ScenarioMember{PersonID: id, Name: names[id],
					FiveHour: part.result(fiveBasis, fiveRate), Weekly: weeklyResult(weekBasis, weekRate, weeks)})
			}
			scenario.FiveHour = total.result(fiveBasis, fiveRate)
			scenario.Weekly = weeklyResult(weekBasis, weekRate, quota.WorstWeekly(allWeeks...))
		}
		report.Scenarios = append(report.Scenarios, scenario)
	}
	return report, nil
}

// demand is an account's recorded official requests.
type demand struct {
	// all and byPerson are the weights over the whole lookback, oldest
	// first; the totals count only the period.
	all      []quota.Demand
	byPerson map[string][]quota.Demand

	tokens         int64
	cost           float64
	requests       int
	personTokens   map[string]int64
	personCost     map[string]float64
	personRequests map[string]int
}

func newDemand(events []storage.DemandEvent, start, end time.Time) demand {
	d := demand{byPerson: map[string][]quota.Demand{}, personTokens: map[string]int64{},
		personCost: map[string]float64{}, personRequests: map[string]int{}}
	for _, e := range events {
		// A third-party model never counts against the quota.
		if e.ThirdParty {
			continue
		}
		cost := attribution.Cost(e.Model, e.Tokens)
		if !e.OccurredAt.Before(start) && e.OccurredAt.Before(end) {
			tokens := e.Tokens.Input + e.Tokens.CachedInput + e.Tokens.CacheWrite + e.Tokens.Output
			d.tokens += tokens
			d.cost += cost
			d.requests++
			d.personTokens[e.PersonID] += tokens
			d.personCost[e.PersonID] += cost
			d.personRequests[e.PersonID]++
		}
		// A request of a model without a price carries no weight.
		if cost > 0 {
			w := quota.Demand{At: e.OccurredAt, Weight: cost}
			d.all = append(d.all, w)
			d.byPerson[e.PersonID] = append(d.byPerson[e.PersonID], w)
		}
	}
	return d
}

// readings are the provider's own reports of a limit window's use. A
// statusLine figure can be older than the moment it was observed, so it
// would blur where a window's use stood. A reading of a window that has not
// started reports its reset a full window away, so it describes no use.
func readings(snaps []quota.Snapshot, key quota.BucketKey, span time.Duration) []quota.Reading {
	var out []quota.Reading
	for _, s := range snaps {
		if s.Source == quota.SourceClaudeStatusLine {
			continue
		}
		for _, b := range s.Buckets {
			if b.Key != key || b.UsedPercent == nil || b.ResetsAt == nil || b.WindowMinutes == nil {
				continue
			}
			if b.ResetsAt.Sub(s.ObservedAt) > span-resetStarted {
				continue
			}
			out = append(out, quota.Reading{ObservedAt: s.ObservedAt, ResetsAt: *b.ResetsAt, UsedPercent: *b.UsedPercent,
				SnapshotPlan: s.PlanType})
		}
	}
	return out
}

func calibrationRows(key quota.BucketKey, cal map[string]quota.Calibration) []syncapi.CalibrationRow {
	var rows []syncapi.CalibrationRow
	for plan, c := range cal {
		rows = append(rows, syncapi.CalibrationRow{Bucket: string(key), Plan: plan, Samples: c.Samples,
			Rate: c.Median, RateMin: c.Min, RateMax: c.Max, CapacityUSD: 100 / c.Median})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Plan < rows[j].Plan })
	return rows
}

// multiples is how much more of a limit Max 20x measured than Max 5x, where
// both are measured.
func multiples(fiveHour, weekly map[string]quota.Calibration) []syncapi.CapacityMultiple {
	out := []syncapi.CapacityMultiple{}
	if k5, b5 := quota.FiveHourRate("max 5x", fiveHour); b5 == quota.BasisMeasured {
		if k20, b20 := quota.FiveHourRate("max 20x", fiveHour); b20 == quota.BasisMeasured {
			small, _ := quota.PlanMultiple("max 5x")
			large, _ := quota.PlanMultiple("max 20x")
			out = append(out, syncapi.CapacityMultiple{Bucket: string(quota.BucketFiveHour), Observed: k5 / k20, Official: large / small})
		}
	}
	if k5, b5 := quota.WeeklyRate("max 5x", weekly); b5 == quota.BasisMeasured {
		if k20, b20 := quota.WeeklyRate("max 20x", weekly); b20 == quota.BasisMeasured {
			out = append(out, syncapi.CapacityMultiple{Bucket: string(quota.BucketWeekly), Observed: k5 / k20})
		}
	}
	return out
}

// inPeriod is whether a window overlaps the period.
func inPeriod(w quota.Window, start, end time.Time) bool {
	return w.ResetsAt.After(start) && w.Start.Before(end)
}

// observedWindows lists the period's 5-hour windows, newest first, each with
// what its plan's measured rate predicts for the demand recorded in it.
func observedWindows(windows []quota.Window, cal map[string]quota.Calibration, start, end time.Time) []syncapi.ObservedWindow {
	out := []syncapi.ObservedWindow{}
	for _, w := range windows {
		if !inPeriod(w, start, end) {
			continue
		}
		ow := syncapi.ObservedWindow{Start: w.Start.UTC(), ResetsAt: w.ResetsAt.UTC(), Plan: w.Plan, MaxPercent: w.MaxUsed,
			Saturated: w.Saturated, CostUSD: w.CostAtMax, Excluded: w.Excluded}
		if !w.FirstFull.IsZero() {
			t := w.FirstFull.UTC()
			ow.FirstFull = &t
		}
		if w.Plan != "" {
			if rate, basis := quota.FiveHourRate(w.Plan, cal); basis == quota.BasisMeasured {
				predicted := w.CostAtMax * rate
				ow.PredictedPercent = &predicted
			}
		}
		out = append(out, ow)
	}
	slices.Reverse(out)
	return out
}

// saturationRows counts the period's windows by plan, and how many of them
// reached 100%.
func saturationRows(key quota.BucketKey, windows []quota.Window, start, end time.Time) []syncapi.SaturationRow {
	counts := map[string]*syncapi.SaturationRow{}
	for _, w := range windows {
		if !inPeriod(w, start, end) {
			continue
		}
		row, ok := counts[w.Plan]
		if !ok {
			row = &syncapi.SaturationRow{Bucket: string(key), Plan: w.Plan}
			counts[w.Plan] = row
		}
		row.Windows++
		if w.Saturated {
			row.Saturated++
		}
	}
	var out []syncapi.SaturationRow
	for _, row := range counts {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plan < out[j].Plan })
	return out
}

// fiveHourPart is one limit's 5-hour replay, its rolling figures in percent
// of the limit.
type fiveHourPart struct {
	replay                              quota.SessionReplay
	rollingPeak, rollingP50, rollingP95 float64
}

// merge combines the parts of separate limits; the rolling figures take the
// worst of them.
func (p fiveHourPart) merge(o fiveHourPart) fiveHourPart {
	return fiveHourPart{replay: p.replay.Merge(o.replay), rollingPeak: math.Max(p.rollingPeak, o.rollingPeak),
		rollingP50: math.Max(p.rollingP50, o.rollingP50), rollingP95: math.Max(p.rollingP95, o.rollingP95)}
}

func (p fiveHourPart) result(basis quota.Basis, rate float64) syncapi.FiveHourResult {
	res := syncapi.FiveHourResult{Basis: string(basis)}
	if basis == quota.BasisInsufficient {
		return res
	}
	res.CapacityUSD = 100 / rate
	res.Fit = quota.Fit(p.replay.PeakPercent)
	res.Windows, res.HitWindows = p.replay.Windows, p.replay.HitWindows
	if !p.replay.FirstHit.IsZero() {
		t := p.replay.FirstHit.UTC()
		res.FirstHit = &t
	}
	res.PeakPercent, res.BlockedPercent = p.replay.PeakPercent, p.replay.BlockedPercent()
	res.RollingPeakPercent, res.RollingP50Percent, res.RollingP95Percent = p.rollingPeak, p.rollingP50, p.rollingP95
	return res
}

func weeklyResult(basis quota.Basis, rate float64, weeks []quota.WeeklyReplay) syncapi.WeeklyResult {
	res := syncapi.WeeklyResult{Basis: string(basis), Windows: []syncapi.WeeklyWindow{}}
	if basis == quota.BasisInsufficient {
		return res
	}
	res.CapacityUSD = 100 / rate
	for _, w := range weeks {
		ww := syncapi.WeeklyWindow{Start: w.Start.UTC(), End: w.End.UTC(), PeakPercent: w.PeakPercent}
		if !w.HitAt.IsZero() {
			t := w.HitAt.UTC()
			ww.HitAt = &t
			res.HitWindows++
		}
		res.PeakPercent = math.Max(res.PeakPercent, w.PeakPercent)
		res.Windows = append(res.Windows, ww)
	}
	res.Fit = quota.Fit(res.PeakPercent)
	return res
}
