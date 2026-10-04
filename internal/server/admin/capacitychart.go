package admin

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

// capacityChart draws a limit's predicted use over the report's period on
// the SVG of the usage charts: a step line for each limit replayed, the
// provider's readings as dots, the 100% line, the borderline, and the
// limits Claude Code logged. X positions are percentages of the width.
type capacityChart struct {
	T             map[string]string
	Title, Bucket string
	Lines         []capacityLine
	// Actual is the readings, each a dot: a zero-length stroke with round
	// caps stays round under the view box's uneven scaling.
	Actual string
	Limits []chartMark
	Plans  []capacityBand
	// Y100 and Y80 are where the limit and the borderline fall, YZero the
	// baseline.
	Y100, Y80, YZero float64
	Borderline       int
	// Top is the axis's top, in percent: 100, or the highest use past it.
	Top                    int
	Points                 []chartPoint
	Ticks                  []time.Time
	TickFormat, TimeFormat string

	start, end int64
}

type capacityLine struct {
	Name, Slot, Path string
	points           [][2]float64
}

// capacityBand is the span a plan was in effect, drawn where the period saw
// more than one.
type capacityBand struct {
	X, W  float64
	Plan  string
	Shade bool
}

// capacityHoverSlices is how many spans a chart's tips divide the period into.
const capacityHoverSlices = 60

// newCapacityCharts is a chart for each scenario that replayed the bucket,
// "fiveHour" or "weekly". Separate limits draw a line for each member, in
// the colors of the model lists; a shared one draws a single line.
func newCapacityCharts(r syncapi.CapacityReport, t map[string]string, bucket string) []capacityChart {
	var charts []capacityChart
	for _, sc := range r.Scenarios {
		var lines []capacityLine
		add := func(name, slot string, five syncapi.FiveHourResult, week syncapi.WeeklyResult) {
			series, basis := five.Series, five.Basis
			if bucket == "weekly" {
				series, basis = week.Series, week.Basis
			}
			if basis != string(quota.BasisInsufficient) {
				lines = append(lines, capacityLine{Name: name, Slot: slot, points: series})
			}
		}
		if sc.Shared {
			add(t["predicted"], "a", sc.FiveHour, sc.Weekly)
		}
		for i, m := range sc.Members {
			add(m.Name, slotFor(i), m.FiveHour, m.Weekly)
		}
		if len(lines) > 0 {
			charts = append(charts, newCapacityChart(r, t, bucket, codeText(t, "scenario", sc.ID), lines))
		}
	}
	return charts
}

func newCapacityChart(r syncapi.CapacityReport, t map[string]string, bucket, title string, lines []capacityLine) capacityChart {
	c := capacityChart{T: t, Title: title, Bucket: bucket, Lines: lines, start: r.Start.Unix(), end: r.End.Unix(),
		TickFormat: "clock", TimeFormat: "clock"}
	if r.End.Sub(r.Start) > 24*time.Hour {
		c.TickFormat, c.TimeFormat = "day", "dayclock"
	}
	c.Ticks = []time.Time{r.Start, r.Start.Add(r.End.Sub(r.Start) / 2), r.End}

	peak := 0.0
	for _, l := range lines {
		for _, p := range l.points {
			peak = math.Max(peak, p[1])
		}
	}
	c.Top = max(100, int(math.Ceil(peak/10))*10)
	c.Borderline = r.BorderlinePercent
	c.Y100, c.Y80, c.YZero = c.y(100), c.y(float64(r.BorderlinePercent)), c.y(0)
	for i := range c.Lines {
		c.Lines[i].Path = c.stepPath(c.Lines[i].points)
	}

	var readings [][2]float64
	kind := usage.LimitWeekly
	if bucket == "fiveHour" {
		kind = usage.LimitFiveHour
		readings = r.Readings
		var d strings.Builder
		for _, p := range readings {
			fmt.Fprintf(&d, "M%.1f,%.1fh0", c.x(p[0])*chartWidth/100, c.y(p[1]))
		}
		c.Actual = d.String()
	}
	for _, h := range r.LimitHits {
		if h.Kind == string(kind) && !h.At.Before(r.Start) && h.At.Before(r.End) {
			c.Limits = append(c.Limits, chartMark{X: c.x(float64(h.At.Unix())), At: h.At})
		}
	}
	c.bands(r.Plans, r.Start, r.End)
	c.hover(readings)
	return c
}

// x is a time, in unix seconds, as a percentage of the width.
func (c *capacityChart) x(at float64) float64 {
	return math.Min(100, math.Max(0, (at-float64(c.start))/float64(c.end-c.start)*100))
}

// y is a use, in percent, as a height in the view box, as the usage charts
// place a value.
func (c *capacityChart) y(percent float64) float64 {
	return chartHeight - percent/float64(c.Top)*(chartHeight-4) - 1
}

// stepPath holds each point's use until the next, which is how the replay
// counts it.
func (c *capacityChart) stepPath(points [][2]float64) string {
	var d strings.Builder
	for i, p := range points {
		x, y := c.x(p[0])*chartWidth/100, c.y(p[1])
		if i == 0 {
			fmt.Fprintf(&d, "M%.1f,%.1f", x, y)
		} else {
			fmt.Fprintf(&d, "H%.1fV%.1f", x, y)
		}
	}
	return d.String()
}

// bands marks the plans of a period that saw a change of plan.
func (c *capacityChart) bands(plans []syncapi.PlanIntervalDTO, start, end time.Time) {
	for _, p := range plans {
		from, to := p.EffectiveAt, end
		if p.EndedAt != nil {
			to = *p.EndedAt
		}
		if from.Before(start) {
			from = start
		}
		if to.After(end) {
			to = end
		}
		if to.After(from) {
			x := c.x(float64(from.Unix()))
			c.Plans = append(c.Plans, capacityBand{X: x, W: c.x(float64(to.Unix())) - x, Plan: p.PlanType})
		}
	}
	if len(c.Plans) < 2 {
		c.Plans = nil
		return
	}
	sort.Slice(c.Plans, func(i, j int) bool { return c.Plans[i].X < c.Plans[j].X })
	for i := range c.Plans {
		c.Plans[i].Shade = i%2 == 1
	}
}

// hover gives each slice of the period a tip with the most use each line and
// the readings reached in it.
func (c *capacityChart) hover(readings [][2]float64) {
	width := float64(c.end-c.start) / capacityHoverSlices
	for i := range capacityHoverSlices {
		from := float64(c.start) + float64(i)*width
		to := from + width
		pt := chartPoint{X: float64(i) * 100 / capacityHoverSlices, W: 100.0 / capacityHoverSlices, At: time.Unix(int64(from), 0)}
		pt.Mid = pt.X + pt.W/2
		for _, l := range c.Lines {
			if v := stepMax(l.points, from, to); v > 0 {
				pt.Rows = append(pt.Rows, chartTipRow{Tokens: percent(v), Name: l.Name, Slot: l.Slot})
			}
		}
		if v, ok := readingMax(readings, from, to); ok {
			pt.Rows = append(pt.Rows, chartTipRow{Tokens: percent(v), Name: c.T["actual"], Slot: "x"})
		}
		c.Points = append(c.Points, pt)
	}
}

// stepMax is the most use a step series held in [from, to).
func stepMax(points [][2]float64, from, to float64) float64 {
	i := sort.Search(len(points), func(i int) bool { return points[i][0] > from })
	most := 0.0
	if i > 0 {
		most = points[i-1][1]
	}
	for ; i < len(points) && points[i][0] < to; i++ {
		most = math.Max(most, points[i][1])
	}
	return most
}

// readingMax is the highest reading in [from, to), if there is one.
func readingMax(readings [][2]float64, from, to float64) (float64, bool) {
	i := sort.Search(len(readings), func(i int) bool { return readings[i][0] >= from })
	most, found := 0.0, false
	for ; i < len(readings) && readings[i][0] < to; i++ {
		most, found = math.Max(most, readings[i][1]), true
	}
	return most, found
}
