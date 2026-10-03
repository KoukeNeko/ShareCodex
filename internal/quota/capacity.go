package quota

import (
	"math"
	"slices"
	"sort"
	"time"
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

// Exceedance marks an interval during which demand exceeded capacity.
type Exceedance struct {
	Start time.Time
	End   time.Time
}

// Above joins contiguous spans exceeding an explicitly supplied capacity threshold.
func Above(spans []DemandSpan, capacity float64) []Exceedance {
	var out []Exceedance
	for _, s := range spans {
		if s.Weight <= capacity {
			continue
		}
		n := len(out)
		if n > 0 && out[n-1].End.Equal(s.Start) {
			out[n-1].End = s.End
		} else {
			out = append(out, Exceedance{Start: s.Start, End: s.End})
		}
	}
	return out
}
