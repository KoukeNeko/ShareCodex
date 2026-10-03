package usage

import (
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/account"
)

type LimitKind string

const (
	LimitFiveHour      LimitKind = "5h"
	LimitWeekly        LimitKind = "weekly"
	LimitModelSpecific LimitKind = "model-specific"
	LimitOverload      LimitKind = "overload"
	LimitProvider429   LimitKind = "provider429"
	LimitAuth          LimitKind = "auth"
	LimitUnknown       LimitKind = "unknown"
)

// LimitEvent is an observed limit hit, refusal, or failure event.
type LimitEvent struct {
	DedupeKey      string
	AccountRefHash string
	DeviceID       string
	PersonID       string
	Provider       account.Provider
	OccurredAt     time.Time
	ObservedAt     time.Time
	SessionID      string
	RequestID      string
	Kind           LimitKind
	Source         string
	Evidence       string
	HTTPStatus     int
}
