// Package attribution splits an account's quota among the people sharing
// it: how much each is allotted, and an estimate of how much each used.
//
// Providers report only the account-wide percentage used. Each person's
// portion of it is estimated from their share of API-equivalent token cost
// in the same window, so the per-person numbers are always estimates.
package attribution

import (
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

// pricing.json holds USD per million tokens, matched by model-name
// substring in file order. It only sets relative weights between models;
// it is not a bill.
//
//go:embed pricing.json
var pricingJSON []byte

type price struct {
	Match       string  `json:"match"`
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cached_input"`
	CacheWrite  float64 `json:"cache_write"`
	Output      float64 `json:"output"`
}

var prices = mustLoadPrices()

func mustLoadPrices() []price {
	var p []price
	if err := json.Unmarshal(pricingJSON, &p); err != nil {
		panic("attribution: invalid pricing.json: " + err.Error())
	}
	return p
}

// Cost is the API-equivalent cost of tokens on a model, in USD.
func Cost(model string, t usage.Tokens) float64 {
	for _, p := range prices {
		if strings.Contains(model, p.Match) {
			return (float64(t.Input)*p.Input +
				float64(t.CachedInput)*p.CachedInput +
				float64(t.CacheWrite)*p.CacheWrite +
				float64(t.Output)*p.Output) / 1e6
		}
	}
	return 0
}

type Share struct {
	// AllottedPercent is the person's portion of the whole window, from
	// their share weight.
	AllottedPercent float64
	// UsedPercent is the estimated portion of the window they consumed.
	UsedPercent float64
}

// Apportion splits usedPercent of one quota window among people. costs is
// each person's API-equivalent cost within the window; weights is each
// member's share weight. When quota was consumed but no one has any cost in
// the window, it is reported as unattributed rather than assigned to anyone.
func Apportion(usedPercent float64, costs, weights map[string]float64) (shares map[string]Share, unattributed float64) {
	shares = make(map[string]Share)

	var totalWeight float64
	for _, w := range weights {
		totalWeight += w
	}
	for person, w := range weights {
		s := shares[person]
		if totalWeight > 0 {
			s.AllottedPercent = w / totalWeight * 100
		}
		shares[person] = s
	}

	var totalCost float64
	for _, c := range costs {
		totalCost += c
	}
	if totalCost == 0 {
		return shares, usedPercent
	}
	for person, c := range costs {
		s := shares[person]
		s.UsedPercent = usedPercent * c / totalCost
		shares[person] = s
	}
	return shares, 0
}
