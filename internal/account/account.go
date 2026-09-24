// Package account describes the shared subscription accounts in the pool and
// which people may use them.
package account

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
)

type Account struct {
	ID       string
	Provider Provider
	// ExternalRefHash identifies the upstream account without storing the
	// email or org ID reported by the provider CLI.
	ExternalRefHash string
	// Hint is a masked form of the provider-reported identifier so an admin
	// can tell accounts apart without the server storing the raw value.
	Hint     string
	Label    string
	PlanType string
}

// Membership links a person to an account they share; Person N:M Account.
// ShareWeight sets the person's allotted portion of the account's quota.
type Membership struct {
	AccountID   string
	PersonID    string
	ShareWeight float64
}

// HashExternalRef is the single place that turns a provider-reported account
// identifier into the value stored and compared across devices.
func HashExternalRef(provider Provider, ref string) string {
	sum := sha256.Sum256([]byte(string(provider) + ":" + ref))
	return hex.EncodeToString(sum[:])
}

// MaskRef hides most of an identifier while keeping enough to recognise it:
// "someone@example.com" becomes "so***@example.com".
func MaskRef(ref string) string {
	local, domain, isEmail := strings.Cut(ref, "@")
	keep := min(2, len(local))
	masked := local[:keep] + "***"
	if isEmail {
		return masked + "@" + domain
	}
	return masked
}

// Observation records which upstream account a device was logged into at a
// point in time, as reported by the provider's official CLI.
type Observation struct {
	DeviceID        string
	Provider        Provider
	ExternalRefHash string
	Hint            string
	PlanType        string
	ObservedAt      time.Time
}

// ResolveAt returns the account a device was using at t: the latest
// observation for that provider at or before t. Events before the first
// observation stay unresolved rather than being guessed.
func ResolveAt(observations []Observation, provider Provider, t time.Time) (string, bool) {
	matching := make([]Observation, 0, len(observations))
	for _, o := range observations {
		if o.Provider == provider {
			matching = append(matching, o)
		}
	}
	sort.Slice(matching, func(i, j int) bool {
		return matching[i].ObservedAt.Before(matching[j].ObservedAt)
	})

	var ref string
	found := false
	for _, o := range matching {
		if o.ObservedAt.After(t) {
			break
		}
		ref, found = o.ExternalRefHash, true
	}
	return ref, found
}
