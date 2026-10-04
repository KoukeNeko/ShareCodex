package quota

import (
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
)

// Demand is recorded workload, not a provider's subscription accounting.
type Demand struct {
	At     time.Time
	Weight float64
}

// DemandSpan is a continuous interval where rolling demand was constant.
type DemandSpan struct {
	Start  time.Time
	End    time.Time
	Weight float64
}

// RollingUsage summarizes the sliding-window workload across [start, end].
type RollingUsage struct {
	Peak   float64
	PeakAt time.Time
	P50    float64
	P90    float64
	P95    float64
	P99    float64
	Spans  []DemandSpan
}

// Rolling measures (t-window, t] and weights percentiles by elapsed time,
// including zero-demand spans. Events before start supply the lookback.
func Rolling(events []Demand, start, end time.Time, window time.Duration) RollingUsage {
	result := RollingUsage{Spans: []DemandSpan{}}
	if !end.After(start) || window <= 0 {
		return result
	}

	type change struct {
		at     time.Time
		weight float64
	}
	var changes []change
	for _, e := range events {
		if e.Weight <= 0 || math.IsNaN(e.Weight) || math.IsInf(e.Weight, 0) {
			continue
		}
		if e.At.Before(end) && e.At.Add(window).After(start) {
			changes = append(changes, change{e.At, e.Weight}, change{e.At.Add(window), -e.Weight})
		}
	}
	slices.SortFunc(changes, func(a, b change) int { return a.at.Compare(b.at) })

	current := 0.0
	i := 0
	for i < len(changes) && !changes[i].at.After(start) {
		current += changes[i].weight
		i++
	}
	current = math.Max(0, current)
	result.Peak, result.PeakAt = current, start
	last := start

	for i < len(changes) && changes[i].at.Before(end) {
		at := changes[i].at
		if at.After(last) {
			result.Spans = append(result.Spans, DemandSpan{Start: last, End: at, Weight: current})
		}
		for i < len(changes) && changes[i].at.Equal(at) {
			current += changes[i].weight
			i++
		}
		current = math.Max(0, current)
		if current > result.Peak {
			result.Peak, result.PeakAt = current, at
		}
		last = at
	}
	result.Spans = append(result.Spans, DemandSpan{Start: last, End: end, Weight: current})

	ranked := slices.Clone(result.Spans)
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].Weight < ranked[j].Weight })

	totalSeconds := end.Sub(start).Seconds()
	percentile := func(p float64) float64 {
		if totalSeconds <= 0 {
			return 0
		}
		threshold := totalSeconds * p
		elapsed := 0.0
		for _, s := range ranked {
			elapsed += s.End.Sub(s.Start).Seconds()
			if elapsed >= threshold {
				return s.Weight
			}
		}
		if len(ranked) > 0 {
			return ranked[len(ranked)-1].Weight
		}
		return 0
	}

	result.P50 = percentile(0.50)
	result.P90 = percentile(0.90)
	result.P95 = percentile(0.95)
	result.P99 = percentile(0.99)
	return result
}

// The limit windows Claude reports, as durations.
const (
	FiveHourSpan = FiveHourMinutes * time.Minute
	WeeklySpan   = WeeklyMinutes * time.Minute
)

// planMultiples is each Claude plan's 5-hour allowance relative to Pro, as
// Anthropic publishes it. Anthropic publishes no weekly multiple.
var planMultiples = map[string]float64{"pro": 1, "max 5x": 5, "max 20x": 20}

// KnownPlans lists the plans PlanMultiple knows, smallest first.
func KnownPlans() []string {
	plans := make([]string, 0, len(planMultiples))
	for p := range planMultiples {
		plans = append(plans, p)
	}
	slices.SortFunc(plans, func(a, b string) int { return int(planMultiples[a] - planMultiples[b]) })
	return plans
}

// NormalizePlan puts a provider's plan string in the form plans are compared
// in: lower case, single spaces.
func NormalizePlan(plan string) string {
	return strings.Join(strings.Fields(strings.ToLower(plan)), " ")
}

// PlanMultiple is a plan's 5-hour allowance relative to Pro; false for a
// plan Anthropic names no multiple for.
func PlanMultiple(plan string) (float64, bool) {
	m, ok := planMultiples[NormalizePlan(plan)]
	return m, ok
}

// Reading is one provider report of a limit window's use. The windows built
// from readings never mix sources whose figures can be stale.
type Reading struct {
	ObservedAt time.Time
	ResetsAt   time.Time
	// UsedPercent is the share of the window's allowance used, 0 to 100.
	UsedPercent float64
	// SnapshotPlan is the plan the reading itself was recorded with, if any.
	SnapshotPlan string
}

// Why a window gives no calibration sample.
const (
	ExcludedUnknownPlan = "unknown_plan"
	ExcludedPlanChanged = "plan_changed"
	ExcludedNoSample    = "no_sample"
	ExcludedLowCost     = "low_cost"
)

const (
	// minSampleUsed keeps near-empty readings, whose rounding dominates the
	// ratio, out of calibration.
	minSampleUsed = 5
	// MinSampleCost is the least demand, in USD, a window must have recorded
	// to calibrate: with less, unrecorded use outweighs it.
	MinSampleCost = 5
)

// Window is one limit window as readings observed it.
type Window struct {
	Start    time.Time
	ResetsAt time.Time
	// Plan is the one plan the whole window ran under; "" when it is unknown
	// or changed.
	Plan    string
	MaxUsed float64
	// MaxAt is when the highest use was first read.
	MaxAt time.Time
	// Saturated is whether any reading reached 100%, FirstFull the first.
	Saturated bool
	FirstFull time.Time
	// CostAtMax is the demand recorded from Start to MaxAt, in USD.
	CostAtMax float64
	// Ratio is the window's measured percent per USD; zero when Excluded
	// says why the window gives no sample.
	Ratio    float64
	Excluded string
}

// resetSlack merges the reset times that readings of one window report: they
// differ by seconds, sometimes a minute.
const resetSlack = 2 * time.Minute

func roundedReset(r Reading) time.Time { return r.ResetsAt.UTC().Round(time.Minute) }

// groupByReset splits readings into the windows they describe, each group
// oldest reading first.
func groupByReset(readings []Reading) [][]Reading {
	sorted := slices.Clone(readings)
	slices.SortStableFunc(sorted, func(a, b Reading) int { return roundedReset(a).Compare(roundedReset(b)) })
	var groups [][]Reading
	for _, r := range sorted {
		if n := len(groups); n > 0 {
			prev := groups[n-1]
			if roundedReset(r).Sub(roundedReset(prev[len(prev)-1])) <= resetSlack {
				groups[n-1] = append(prev, r)
				continue
			}
		}
		groups = append(groups, []Reading{r})
	}
	for _, g := range groups {
		slices.SortStableFunc(g, func(a, b Reading) int { return a.ObservedAt.Compare(b.ObservedAt) })
	}
	return groups
}

// resetOf is the reset time a group of readings, oldest first, settles on:
// the newest reading's.
func resetOf(group []Reading) time.Time { return roundedReset(group[len(group)-1]) }

// sumBetween adds up the weight of demand, oldest first, from through to
// inclusive.
func sumBetween(demand []Demand, from, to time.Time) float64 {
	i := sort.Search(len(demand), func(i int) bool { return !demand[i].At.Before(from) })
	var sum float64
	for ; i < len(demand) && !demand[i].At.After(to); i++ {
		sum += demand[i].Weight
	}
	return sum
}

// measure fills in what a window's readings, oldest first, show of it. A
// window not already excluded gives a sample from its last reading below
// 100%: the figure is exact there, where a saturated one only says "at
// least".
func measure(w Window, readings []Reading, demand []Demand) Window {
	for _, r := range readings {
		if w.MaxAt.IsZero() || r.UsedPercent > w.MaxUsed {
			w.MaxUsed, w.MaxAt = r.UsedPercent, r.ObservedAt
		}
		if r.UsedPercent >= 100 {
			w.Saturated = true
			if w.FirstFull.IsZero() {
				w.FirstFull = r.ObservedAt
			}
		}
	}
	w.CostAtMax = sumBetween(demand, w.Start, w.MaxAt)
	if w.Excluded != "" {
		return w
	}
	var sample *Reading
	for j := len(readings) - 1; j >= 0; j-- {
		if u := readings[j].UsedPercent; u >= minSampleUsed && u < 100 {
			sample = &readings[j]
			break
		}
	}
	if sample == nil {
		w.Excluded = ExcludedNoSample
		return w
	}
	cost := sumBetween(demand, w.Start, sample.ObservedAt)
	if cost < MinSampleCost {
		w.Excluded = ExcludedLowCost
		return w
	}
	w.Ratio = sample.UsedPercent / cost
	return w
}

// FiveHourWindows builds the 5-hour windows readings describe. planAt gives
// the plan at a time, given the plan a reading was recorded with; a window
// counts for a plan only if it ran under that plan from its start to its
// last reading. demand is chronological.
func FiveHourWindows(readings []Reading, planAt func(t time.Time, snapshotPlan string) string, demand []Demand) []Window {
	var out []Window
	for _, group := range groupByReset(readings) {
		w := Window{ResetsAt: resetOf(group)}
		w.Start = w.ResetsAt.Add(-FiveHourSpan)
		plan := planAt(w.Start, group[0].SnapshotPlan)
		for _, r := range group {
			if p := planAt(r.ObservedAt, r.SnapshotPlan); p != plan {
				plan, w.Excluded = "", ExcludedPlanChanged
				break
			}
		}
		switch {
		case w.Excluded != "":
		case plan == "":
			w.Excluded = ExcludedUnknownPlan
		default:
			w.Plan = plan
		}
		out = append(out, measure(w, group, demand))
	}
	return out
}

// WeeklyWindows builds the weekly windows readings describe. A plan change
// restarts the week's use with the reset time unchanged, so each plan
// interval within a week is a window of its own, starting no earlier than
// the interval. Only an interval, not a reading's own plan, is known to
// mark such a restart. demand is chronological.
func WeeklyWindows(readings []Reading, intervals []account.PlanInterval, demand []Demand) []Window {
	var out []Window
	for _, group := range groupByReset(readings) {
		type span struct {
			interval account.PlanInterval
			known    bool
			readings []Reading
		}
		var spans []*span
		for _, r := range group {
			in, known := account.IntervalAt(intervals, r.ObservedAt)
			i := slices.IndexFunc(spans, func(s *span) bool { return s.known == known && s.interval.ID == in.ID })
			if i < 0 {
				spans = append(spans, &span{interval: in, known: known})
				i = len(spans) - 1
			}
			spans[i].readings = append(spans[i].readings, r)
		}
		resets := resetOf(group)
		for _, s := range spans {
			w := Window{ResetsAt: resets, Start: resets.Add(-WeeklySpan)}
			if s.known {
				w.Plan = NormalizePlan(s.interval.PlanType)
				if s.interval.EffectiveAt.After(w.Start) {
					w.Start = s.interval.EffectiveAt
				}
			} else {
				w.Excluded = ExcludedUnknownPlan
			}
			out = append(out, measure(w, s.readings, demand))
		}
	}
	return out
}

// Calibration is the measured percent of a limit used per USD of recorded
// demand, over a plan's samples.
type Calibration struct {
	Samples int
	Median  float64
	Min     float64
	Max     float64
}

// Calibrate summarizes the windows that gave a sample, by plan.
func Calibrate(windows []Window) map[string]Calibration {
	ratios := map[string][]float64{}
	for _, w := range windows {
		if w.Excluded == "" && w.Ratio > 0 {
			ratios[w.Plan] = append(ratios[w.Plan], w.Ratio)
		}
	}
	out := make(map[string]Calibration, len(ratios))
	for plan, rs := range ratios {
		slices.Sort(rs)
		median := rs[len(rs)/2]
		if len(rs)%2 == 0 {
			median = (rs[len(rs)/2-1] + rs[len(rs)/2]) / 2
		}
		out[plan] = Calibration{Samples: len(rs), Median: median, Min: rs[0], Max: rs[len(rs)-1]}
	}
	return out
}

// Basis says how a plan's rate was obtained.
type Basis string

const (
	// BasisMeasured is the plan's own samples.
	BasisMeasured Basis = "measured"
	// BasisDerived is another plan's samples scaled by Anthropic's published
	// multiple between the plans.
	BasisDerived      Basis = "derived"
	BasisInsufficient Basis = "insufficient"
)

// The samples a plan needs for its own median to count as measured. A week
// gives one sample, so one is all a weekly rate can have.
const (
	MinFiveHourSamples = 2
	MinWeeklySamples   = 1
)

// FiveHourRate is the percent of a plan's 5-hour limit used per USD of
// demand. Without enough samples of its own, it is derived from the plan with
// the most samples, through the published multiple between them.
func FiveHourRate(plan string, cal map[string]Calibration) (float64, Basis) {
	plan = NormalizePlan(plan)
	if c := cal[plan]; c.Samples >= MinFiveHourSamples {
		return c.Median, BasisMeasured
	}
	target, ok := PlanMultiple(plan)
	if !ok {
		return 0, BasisInsufficient
	}
	var best string
	for p, c := range cal {
		if _, known := PlanMultiple(p); !known {
			continue
		}
		if b, found := cal[best]; !found || c.Samples > b.Samples || (c.Samples == b.Samples && p < best) {
			best = p
		}
	}
	source, found := cal[best]
	if !found {
		return 0, BasisInsufficient
	}
	multiple, _ := PlanMultiple(best)
	return source.Median * multiple / target, BasisDerived
}

// WeeklyRate is the percent of a plan's weekly limit used per USD of demand.
// No weekly multiple between plans is published, so a plan without samples
// has no rate.
func WeeklyRate(plan string, cal map[string]Calibration) (float64, Basis) {
	if c := cal[NormalizePlan(plan)]; c.Samples >= MinWeeklySamples {
		return c.Median, BasisMeasured
	}
	return 0, BasisInsufficient
}

// How a replay's peak use of a limit reads for planning. The thresholds are
// a rule of thumb, not a provider's.
const (
	FitSafe       = "safe"
	FitBorderline = "borderline"
	FitOver       = "over"
)

const borderlinePercent = 80

// Fit classifies the highest use of a limit, in percent.
func Fit(peakPercent float64) string {
	switch {
	case peakPercent > 100:
		return FitOver
	case peakPercent >= borderlinePercent:
		return FitBorderline
	}
	return FitSafe
}

// SessionReplay is recorded demand replayed against a session limit.
type SessionReplay struct {
	Windows    int
	HitWindows int
	FirstHit   time.Time
	// PeakPercent is the most use of a window's allowance, which can pass 100.
	PeakPercent float64
	// Total is the demand in the replayed windows and Blocked the part of it
	// that came after its window's limit was hit, which the provider would
	// have refused.
	Total   float64
	Blocked float64
}

// BlockedPercent is the share of demand that came after a limit hit.
func (r SessionReplay) BlockedPercent() float64 {
	if r.Total <= 0 {
		return 0
	}
	return r.Blocked / r.Total * 100
}

// Merge adds the replays of separate limits, each person's own.
func (r SessionReplay) Merge(o SessionReplay) SessionReplay {
	r.Windows += o.Windows
	r.HitWindows += o.HitWindows
	if r.FirstHit.IsZero() || (!o.FirstHit.IsZero() && o.FirstHit.Before(r.FirstHit)) {
		r.FirstHit = o.FirstHit
	}
	r.PeakPercent = math.Max(r.PeakPercent, o.PeakPercent)
	r.Total += o.Total
	r.Blocked += o.Blocked
	return r
}

// ReplaySessions replays demand against a limit of capacity per session. A
// session opens with the first request after the previous one ended, and
// lasts window. Only sessions that open from from, before to, are counted;
// earlier demand only sets where sessions begin. The request that takes a
// session past capacity is the hit; demand after it is blocked. Recorded
// demand stops where the provider really refused work, so a replay can
// understate what was wanted.
func ReplaySessions(demand []Demand, capacity float64, window time.Duration, from, to time.Time) SessionReplay {
	var r SessionReplay
	if capacity <= 0 {
		return r
	}
	stream := slices.Clone(demand)
	slices.SortStableFunc(stream, func(a, b Demand) int { return a.At.Compare(b.At) })

	var (
		end     time.Time
		used    float64
		counted bool
		hit     bool
	)
	closeSession := func() {
		if counted {
			r.PeakPercent = math.Max(r.PeakPercent, used/capacity*100)
		}
	}
	for _, d := range stream {
		if d.Weight <= 0 {
			continue
		}
		if end.IsZero() || !d.At.Before(end) {
			closeSession()
			end = d.At.Add(window)
			used, hit = 0, false
			counted = !d.At.Before(from) && d.At.Before(to)
			if counted {
				r.Windows++
			}
		}
		if !counted {
			continue
		}
		r.Total += d.Weight
		if hit {
			r.Blocked += d.Weight
		}
		used += d.Weight
		if !hit && used > capacity {
			hit = true
			r.HitWindows++
			if r.FirstHit.IsZero() {
				r.FirstHit = d.At
			}
		}
	}
	closeSession()
	return r
}

// WeeklyReplay is recorded demand replayed against one week's limit.
type WeeklyReplay struct {
	Start       time.Time
	End         time.Time
	PeakPercent float64
	// HitAt is when the week's use first passed 100%; zero if it did not.
	HitAt time.Time
}

// ReplayWeekly replays demand against a weekly limit of capacity for each
// week, one reset after another from anchor, that overlaps [from, to).
func ReplayWeekly(demand []Demand, capacity float64, anchor, from, to time.Time) []WeeklyReplay {
	if capacity <= 0 || !to.After(from) {
		return nil
	}
	// The first reset after from.
	steps := from.Sub(anchor) / WeeklySpan
	if from.Sub(anchor)%WeeklySpan < 0 {
		steps--
	}
	var out []WeeklyReplay
	for end := anchor.Add((steps + 1) * WeeklySpan); end.Add(-WeeklySpan).Before(to); end = end.Add(WeeklySpan) {
		w := WeeklyReplay{Start: end.Add(-WeeklySpan), End: end}
		i := sort.Search(len(demand), func(i int) bool { return !demand[i].At.Before(w.Start) })
		var used float64
		for ; i < len(demand) && demand[i].At.Before(end); i++ {
			used += demand[i].Weight
			if w.HitAt.IsZero() && used > capacity {
				w.HitAt = demand[i].At
			}
		}
		w.PeakPercent = used / capacity * 100
		out = append(out, w)
	}
	return out
}

// WorstWeekly combines the replays of separate limits over the same weeks:
// each week's highest use and earliest hit.
func WorstWeekly(replays ...[]WeeklyReplay) []WeeklyReplay {
	var out []WeeklyReplay
	for _, r := range replays {
		if out == nil {
			out = slices.Clone(r)
			continue
		}
		for i := range out {
			out[i].PeakPercent = math.Max(out[i].PeakPercent, r[i].PeakPercent)
			if out[i].HitAt.IsZero() || (!r[i].HitAt.IsZero() && r[i].HitAt.Before(out[i].HitAt)) {
				out[i].HitAt = r[i].HitAt
			}
		}
	}
	return out
}
