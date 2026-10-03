package quota

import (
	"testing"
	"time"
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

	// Above capacity 25:
	// should exceed during [t0+1h, t0+3h) and [t0+4h, t0+6h)
	ex := Above(r.Spans, 25)
	if len(ex) != 2 {
		t.Fatalf("got %d exceedances, want 2", len(ex))
	}
	if !ex[0].Start.Equal(t0.Add(1*time.Hour)) || !ex[0].End.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("exceedance 0: %v..%v", ex[0].Start, ex[0].End)
	}
	if !ex[1].Start.Equal(t0.Add(4*time.Hour)) || !ex[1].End.Equal(t0.Add(6*time.Hour)) {
		t.Errorf("exceedance 1: %v..%v", ex[1].Start, ex[1].End)
	}

	// Contiguous exceedances join together
	// If capacity is 12, then:
	// [t0+1h, t0+3h) is 30
	// [t0+3h, t0+4h) is 20
	// [t0+4h, t0+6h) is 35
	// [t0+6h, t0+9h) is 15
	// They should join from t0+1h to t0+9h into a single exceedance!
	exJoined := Above(r.Spans, 12)
	if len(exJoined) != 1 {
		t.Fatalf("got %d joined exceedances, want 1", len(exJoined))
	}
	if !exJoined[0].Start.Equal(t0.Add(1*time.Hour)) || !exJoined[0].End.Equal(t0.Add(9*time.Hour)) {
		t.Errorf("exJoined: %v..%v", exJoined[0].Start, exJoined[0].End)
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
