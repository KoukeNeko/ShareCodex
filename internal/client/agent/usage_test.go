package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/oauthusage"
)

// A sign-in Anthropic throttles is not read again until it allows; other
// failures, and other sign-ins, are read at the next probe as before.
func TestUsagePausesOnlyAThrottledSignIn(t *testing.T) {
	a := &Agent{}
	a.pauseUsage("cli:max", &oauthusage.ThrottledError{Status: "429 Too Many Requests", RetryAfter: time.Hour})
	a.pauseUsage("login:pro", errors.New("read Claude usage: 500 Internal Server Error"))

	if !a.usagePaused("cli:max") {
		t.Error("a throttled sign-in must wait")
	}
	if a.usagePaused("login:pro") || a.usagePaused("cli:other") {
		t.Error("only the throttled sign-in waits")
	}
	a.usageRetryAt["cli:max"] = time.Now().Add(-time.Second)
	if a.usagePaused("cli:max") {
		t.Error("once the wait is over the sign-in is read again")
	}
}
