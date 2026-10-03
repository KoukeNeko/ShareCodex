package query

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/attribution"
	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

// AccountCapacity builds a historical capacity and replay report for one account over a period.
func AccountCapacity(ctx context.Context, st *storage.Store, accountID string, period Period, ratio float64, now time.Time) (syncapi.CapacityReport, error) {
	if ratio <= 0 {
		ratio = 4.0
	}
	acc, err := st.Account(ctx, accountID)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}
	members, err := st.Members(ctx, accountID)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}

	start := now.Add(-period.Span())
	end := now
	window := 5 * time.Hour

	// Fetch demand events from start - 5h to end for rolling lookback.
	events, err := st.AccountDemandEvents(ctx, accountID, start.Add(-window), end)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}

	var totalTokens int64
	var totalCostUSD float64
	combinedDemands := make([]quota.Demand, 0, len(events))
	personDemands := make(map[string][]quota.Demand)
	personTokens := make(map[string]int64)
	personCost := make(map[string]float64)
	personRequests := make(map[string]int)

	for _, e := range events {
		tokens := e.Tokens.Input + e.Tokens.CachedInput + e.Tokens.CacheWrite + e.Tokens.Output
		if e.OccurredAt.After(start) || e.OccurredAt.Equal(start) {
			totalTokens += tokens
		}
		if e.ThirdParty {
			// Third party requests do not count toward subscription quota demand.
			continue
		}
		cost := attribution.Cost(e.Model, e.Tokens)
		if e.OccurredAt.After(start) || e.OccurredAt.Equal(start) {
			totalCostUSD += cost
			personTokens[e.PersonID] += tokens
			personCost[e.PersonID] += cost
			personRequests[e.PersonID]++
		}
		d := quota.Demand{At: e.OccurredAt, Weight: cost}
		combinedDemands = append(combinedDemands, d)
		personDemands[e.PersonID] = append(personDemands[e.PersonID], d)
	}

	combinedRolling := quota.Rolling(combinedDemands, start, end, window)

	// Fetch snapshots to assess saturation and empirical capacity.
	snapshots, err := st.HistoricalSnapshots(ctx, accountID, start, end)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}

	var satStats syncapi.SaturationStatsDTO
	var maxObserved5hCost float64
	for _, s := range snapshots {
		for _, b := range s.Buckets {
			if b.UsedPercent == nil {
				continue
			}
			if b.Key == quota.BucketFiveHour {
				if *b.UsedPercent >= 100 {
					satStats.FiveHourSaturatedCount++
					if satStats.FirstFiveHour100 == nil || s.ObservedAt.Before(*satStats.FirstFiveHour100) {
						t := s.ObservedAt
						satStats.FirstFiveHour100 = &t
					}
				}
			} else if b.Key == quota.BucketWeekly {
				if *b.UsedPercent >= 100 {
					satStats.WeeklySaturatedCount++
					if satStats.FirstWeekly100 == nil || s.ObservedAt.Before(*satStats.FirstWeekly100) {
						t := s.ObservedAt
						satStats.FirstWeekly100 = &t
					}
				}
			}
		}
	}

	// Base 20x capacity: estimate from observed 100% saturation or peak demand.
	maxObserved5hCost = combinedRolling.Peak
	baseCapacity := maxObserved5hCost
	if baseCapacity <= 0 {
		baseCapacity = 1.0 // fallback minimal floor
	}
	fiveHourCap5x := baseCapacity / ratio
	fiveHourCap20x := baseCapacity

	makeScenario := func(name, label string, capVal, peakVal float64, spans []quota.DemandSpan) syncapi.CapacityScenario {
		pct := 0.0
		headroom := 0.0
		fit := "fit"
		if capVal > 0 {
			pct = (peakVal / capVal) * 100
			if pct > 100 {
				headroom = 0
				fit = "exceeded"
			} else {
				headroom = 100 - pct
				if pct >= 80 {
					fit = "borderline"
				}
			}
		}
		exceedances := quota.Above(spans, capVal)
		exSpans := make([]syncapi.Span, 0, len(exceedances))
		for _, ex := range exceedances {
			exSpans = append(exSpans, syncapi.Span{Start: ex.Start, End: ex.End})
		}
		return syncapi.CapacityScenario{
			Name:        name,
			Label:       label,
			Capacity:    math.Round(capVal*100) / 100,
			PeakDemand:  math.Round(peakVal*100) / 100,
			PeakPercent: math.Round(pct*10) / 10,
			Headroom:    math.Round(headroom*10) / 10,
			Exceedances: exSpans,
			Fit:         fit,
		}
	}

	accountScenarios := []syncapi.CapacityScenario{
		makeScenario("shared_5x", "Shared Max 5x", fiveHourCap5x, combinedRolling.Peak, combinedRolling.Spans),
		makeScenario("shared_20x", "Shared Max 20x", fiveHourCap20x, combinedRolling.Peak, combinedRolling.Spans),
	}

	// Per-person rolling usage and scenarios.
	memberCapacities := make([]syncapi.PersonCapacity, 0, len(members))
	for _, m := range members {
		pRoll := quota.Rolling(personDemands[m.PersonID], start, end, window)
		pScenarios := []syncapi.CapacityScenario{
			makeScenario("separate_5x", "Separate Max 5x", fiveHourCap5x, pRoll.Peak, pRoll.Spans),
		}
		memberCapacities = append(memberCapacities, syncapi.PersonCapacity{
			PersonID:    m.PersonID,
			Name:        m.Name,
			Tokens:      personTokens[m.PersonID],
			CostUSD:     personCost[m.PersonID],
			Requests:    personRequests[m.PersonID],
			PeakRolling: math.Round(pRoll.Peak*100) / 100,
			PeakAt:      pRoll.PeakAt,
			P50:         math.Round(pRoll.P50*100) / 100,
			P90:         math.Round(pRoll.P90*100) / 100,
			P95:         math.Round(pRoll.P95*100) / 100,
			Scenarios:   pScenarios,
		})
	}
	sort.Slice(memberCapacities, func(i, j int) bool {
		return memberCapacities[i].CostUSD > memberCapacities[j].CostUSD
	})

	// Fetch limit events.
	limitEvents, err := st.HistoricalLimitEvents(ctx, accountID, start, end)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}
	limitDTOs := make([]syncapi.LimitEventDTO, 0, len(limitEvents))
	for _, l := range limitEvents {
		limitDTOs = append(limitDTOs, syncapi.LimitEventDTO{
			OccurredAt: l.OccurredAt,
			PersonName: l.PersonName,
			Kind:       string(l.Kind),
			Source:     l.Source,
			Evidence:   l.Evidence,
			HTTPStatus: l.HTTPStatus,
		})
	}

	// Fetch plan history.
	plans, err := st.PlanHistory(ctx, accountID)
	if err != nil {
		return syncapi.CapacityReport{}, err
	}
	planDTOs := make([]syncapi.PlanIntervalDTO, 0, len(plans))
	for _, p := range plans {
		planDTOs = append(planDTOs, syncapi.PlanIntervalDTO{
			ID:          p.ID,
			PlanType:    p.PlanType,
			EffectiveAt: p.EffectiveAt,
			EndedAt:     p.EndedAt,
			Reason:      p.Reason,
			Source:      p.Source,
			Precision:   p.Precision,
		})
	}

	notes := []string{
		"Rolling 5h workload is evaluated continuously over (t-5h, t] weighted by API-equivalent token cost.",
		"Max 5x capacity is modeled using the configured ratio (default 4.0x) relative to observed peak demand.",
		"Third-party models are excluded from quota demand calculations.",
		"Replay simulates recorded completed work and does not account for work blocked during provider limit saturation.",
	}

	return syncapi.CapacityReport{
		AccountID:       acc.ID,
		AccountLabel:    acc.Label,
		Provider:        string(acc.Provider),
		CurrentPlan:     acc.PlanType,
		Period:          period.ID,
		Start:           start.UTC(),
		End:             end.UTC(),
		Ratio:           ratio,
		TotalTokens:     totalTokens,
		TotalCostUSD:    math.Round(totalCostUSD*100) / 100,
		CombinedPeak:    math.Round(combinedRolling.Peak*100) / 100,
		CombinedPeakAt:  combinedRolling.PeakAt,
		CombinedP50:     math.Round(combinedRolling.P50*100) / 100,
		CombinedP95:     math.Round(combinedRolling.P95*100) / 100,
		Members:         memberCapacities,
		Scenarios:       accountScenarios,
		ObservedPlans:   planDTOs,
		LimitEvents:     limitDTOs,
		SaturationStats: satStats,
		DataNotes:       notes,
	}, nil
}
