package admin

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/quota"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

// The account cards the overview and the public dashboard share. Views
// carry everything a template needs, so the templates only lay it out.

type accountView struct {
	Provider, Label, PlanType string
	// FiveHour and Weekly are the windows' use for sorting by them; -1
	// when unknown.
	FiveHour, Weekly float64
	// Hint is the masked email, shown only in the console.
	Hint        string
	ActiveUsers []syncapi.ActiveUser
	Buckets     []bucketView
}

type bucketView struct {
	Name string
	// Expired is a window whose last reading has reset: its use is unknown.
	Expired     bool
	UsedPercent float64
	Tone        string
	ResetsAt    *time.Time
	ObservedAt  time.Time

	Members      []memberView
	Unattributed float64
	Models       []modelView
	ThirdParty   []modelView
	Chart        *chartView
}

type memberView struct {
	syncapi.MemberShare
	Over     bool
	Segments []segment
}

// segment is one model's part of a bar; Slot picks its color class.
type segment struct {
	Width float64
	Slot  string
}

type modelView struct {
	syncapi.ModelUsage
	Name, Via, Title, Slot string
	// Bar is the bar's length in percent.
	Bar float64
}

// modelSlots is how many models get their own color; the rest share one,
// as in the desktop popup.
const modelSlots = 4

func slotFor(rank int) string {
	if rank < modelSlots {
		return strconv.Itoa(rank + 1)
	}
	return "o"
}

func modelKey(model, gateway string) string { return gateway + "\x00" + model }

func accountViews(o syncapi.Overview, hints map[string]string, now time.Time, t map[string]string) []accountView {
	out := make([]accountView, 0, len(o.Accounts))
	for _, a := range o.Accounts {
		av := accountView{Provider: a.Provider, Label: a.Label, PlanType: a.PlanType, Hint: hints[a.ID], ActiveUsers: a.ActiveUsers,
			FiveHour: -1, Weekly: -1}
		for _, b := range a.Buckets {
			bv := newBucketView(b, now, t)
			if !bv.Expired {
				switch b.Key {
				case string(quota.BucketFiveHour):
					av.FiveHour = b.UsedPercent
				case string(quota.BucketWeekly):
					av.Weekly = b.UsedPercent
				}
			}
			av.Buckets = append(av.Buckets, bv)
		}
		out = append(out, av)
	}
	return out
}

func newBucketView(b syncapi.BucketOverview, now time.Time, t map[string]string) bucketView {
	bv := bucketView{
		Name:         bucketName(b.Key, b.WindowMinutes, t),
		Expired:      b.Reset || (b.ResetsAt != nil && !b.ResetsAt.After(now)),
		UsedPercent:  b.UsedPercent,
		ResetsAt:     b.ResetsAt,
		ObservedAt:   b.ObservedAt,
		Unattributed: b.UnattributedPercent,
	}
	switch {
	case b.UsedPercent >= 90:
		bv.Tone = "danger"
	case b.UsedPercent >= 70:
		bv.Tone = "warn"
	}

	slots := map[string]string{}
	var quotaKeys, thirdKeys []string
	var quotaTokens int64
	var quotaPercent float64
	for i, m := range b.Models {
		slots[modelKey(m.Model, m.Gateway)] = slotFor(i)
		quotaKeys = append(quotaKeys, modelKey(m.Model, m.Gateway))
		quotaTokens += m.Tokens
		quotaPercent += m.UsedPercent
		bv.Models = append(bv.Models, newModelView(m, slotFor(i), m.UsedPercent))
	}
	var thirdTokens int64
	for _, m := range b.ThirdPartyModels {
		thirdTokens += m.Tokens
	}
	for i, m := range b.ThirdPartyModels {
		thirdKeys = append(thirdKeys, modelKey(m.Model, m.Gateway))
		// Third-party bars use the quota's models' scale, the share a
		// token of theirs used, so a few million tokens read as small
		// beside a billion; without that scale, their share of the
		// third-party tokens.
		var bar float64
		if quotaTokens > 0 && quotaPercent > 0 {
			bar = float64(m.Tokens) / float64(quotaTokens) * quotaPercent
		} else if thirdTokens > 0 {
			bar = float64(m.Tokens) / float64(thirdTokens) * 100
		}
		bv.ThirdParty = append(bv.ThirdParty, newModelView(m, slotFor(i), bar))
	}

	for _, m := range b.Members {
		mv := memberView{MemberShare: m, Over: m.UsedPercent > m.AllottedPercent+0.5}
		for _, mm := range m.Models {
			slot, ok := slots[modelKey(mm.Model, mm.Gateway)]
			if !ok {
				slot = "o"
			}
			mv.Segments = append(mv.Segments, segment{Width: mm.UsedPercent, Slot: slot})
		}
		bv.Members = append(bv.Members, mv)
	}
	// Drawn even without usage, so a quiet window still shows its time axis.
	if b.Timeline != nil {
		bv.Chart = newChart(*b.Timeline, quotaKeys, thirdKeys, now)
	}
	return bv
}

func newModelView(m syncapi.ModelUsage, slot string, bar float64) modelView {
	name, via, title := modelName(m.Model, m.Gateway)
	return modelView{ModelUsage: m, Name: name, Via: via, Title: title, Slot: slot, Bar: bar}
}

var gatewayTags = map[string]struct{ tag, name string }{
	usage.GatewayOpenCodex: {"OCX", "OpenCodex"},
	usage.GatewayOllama:    {"Ollama", "Ollama"},
}

var (
	// OpenCodex names the models it routes to Claude clients
	// ocx-claude-<service>--<model> (older builds claude-ocx-…), and lists
	// them in Codex as <service>/<model>.
	openCodexClaudeModel = regexp.MustCompile(`^(?:ocx-claude|claude-ocx)-(.+?)--(.+)$`)
	openCodexCodexModel  = regexp.MustCompile(`^([^/]+)/(.+)$`)
)

// modelName makes a model ID readable and tags the gateway it went
// through, as the desktop popup does; the full ID stays in the title.
func modelName(id, gateway string) (name, via, title string) {
	m := openCodexClaudeModel.FindStringSubmatch(id)
	if m == nil && gateway == usage.GatewayOpenCodex {
		m = openCodexCodexModel.FindStringSubmatch(id)
	}
	if m != nil {
		gateway = usage.GatewayOpenCodex
	}
	g, ok := gatewayTags[gateway]
	if !ok {
		return id, "", id
	}
	if m == nil {
		return id, g.tag, g.name + " · " + id
	}
	return m[2], g.tag, g.name + " · " + m[1] + " · " + id
}

func bucketName(key string, windowMinutes int, t map[string]string) string {
	switch key {
	case string(quota.BucketFiveHour):
		return t["fiveHour"]
	case string(quota.BucketWeekly):
		return t["weekly"]
	}
	// A weekly limit on one model family, such as weekly_fable.
	if family := quota.BucketKey(key).ModelFamily(); family != "" {
		return t["weekly"] + " · " + strings.ToUpper(family[:1]) + family[1:]
	}
	if windowMinutes%1440 == 0 && windowMinutes > 0 {
		return fmt.Sprintf(t["days"], windowMinutes/1440)
	}
	if windowMinutes%60 == 0 && windowMinutes > 0 {
		return fmt.Sprintf(t["hours"], windowMinutes/60)
	}
	return fmt.Sprintf(t["minutes"], windowMinutes)
}

// chartView draws a timeline the way the desktop popup does: a line per
// model, about 60 points across, in two modes the viewer switches between.
// X positions are percentages of the width; the SVG's view box is
// chartWidth by chartHeight.
type chartView struct {
	// Amount holds the tokens used within each point's span; Cumulative
	// the running totals, which start again from zero where a window reset.
	Amount, Cumulative chartMode
	Resets             []chartMark
	Ticks              []time.Time
	// TickFormat and TimeFormat are timeFormats keys: a chart of more
	// than a day labels its axis with dates, and keeps the time in reset
	// labels and hover tips.
	TickFormat, TimeFormat string
}

type chartMode struct {
	Lines []chartLine
	// Top is the axis's top value; 0 when nothing was used.
	Top    int64
	Points []chartPoint
}

type chartLine struct {
	Path   string
	Slot   string
	Dashed bool
}

type chartMark struct {
	X  float64
	At time.Time
}

// chartPoint is one point's hover area, with the time its values are as of
// (a running total) or the span they cover (an amount), and each model's
// value.
type chartPoint struct {
	X, W  float64
	At    time.Time
	Until *time.Time
	Lines []string
}

const (
	chartWidth  = 1000
	chartHeight = 100
	chartPoints = 60
)

// quotaKeys and thirdKeys are the model keys of the card's two model lists,
// its legends, in order; a line takes its model's color and place there.
func newChart(tl syncapi.Timeline, quotaKeys, thirdKeys []string, now time.Time) *chartView {
	binDur := time.Duration(tl.BinMinutes) * time.Minute
	if tl.Bins <= 0 || binDur <= 0 {
		return nil
	}
	start := tl.Start
	// Bins after now have not happened yet, so the lines stop at now.
	lastBin := min(tl.Bins-1, max(0, int(now.Sub(start)/binDur)))
	group := max(1, (tl.Bins+chartPoints-1)/chartPoints)
	lastPoint := lastBin / group
	// Each point's bins are [from(p), end(p)).
	from := func(point int) int { return point * group }
	end := func(point int) int { return min((point+1)*group, lastBin+1) }

	resetBins := map[int]bool{}
	for _, r := range tl.Resets {
		resetBins[int(r.Sub(start)/binDur)] = true
	}

	type series struct {
		model, gateway string
		third          bool
		rank           int
		perBin         []int64
	}
	var all []*series
	byKey := map[string]*series{}
	for _, p := range tl.Points {
		if p.Bin < 0 || p.Bin > lastBin {
			continue
		}
		key := strconv.FormatBool(p.ThirdParty) + "\x00" + modelKey(p.Model, p.Gateway)
		s := byKey[key]
		if s == nil {
			legend := quotaKeys
			if p.ThirdParty {
				legend = thirdKeys
			}
			rank := slices.Index(legend, modelKey(p.Model, p.Gateway))
			if rank < 0 {
				rank = len(legend)
			}
			s = &series{model: p.Model, gateway: p.Gateway, third: p.ThirdParty, rank: rank, perBin: make([]int64, lastBin+1)}
			byKey[key] = s
			all = append(all, s)
		}
		s.perBin[p.Bin] += p.Tokens
	}
	// The quota's models first, each list in its own (legend) order.
	slices.SortStableFunc(all, func(a, b *series) int {
		if a.third != b.third {
			if a.third {
				return 1
			}
			return -1
		}
		return a.rank - b.rank
	})

	amounts := make([][]int64, len(all))
	totals := make([][]int64, len(all))
	for i, s := range all {
		var total int64
		running := make([]int64, lastBin+1)
		for bin, v := range s.perBin {
			if resetBins[bin] {
				total = 0
			}
			total += v
			running[bin] = total
		}
		amounts[i] = make([]int64, lastPoint+1)
		totals[i] = make([]int64, lastPoint+1)
		for p := range lastPoint + 1 {
			for _, v := range s.perBin[from(p):end(p)] {
				amounts[i][p] += v
			}
			totals[i][p] = running[end(p)-1]
		}
	}

	x := func(bin float64) float64 { return bin / float64(tl.Bins) * 100 }
	at := func(bin int) time.Time { return minTime(start.Add(time.Duration(bin)*binDur), now) }
	mode := func(values [][]int64, cumulative bool) chartMode {
		var peak int64
		for _, vs := range values {
			for _, v := range vs {
				peak = max(peak, v)
			}
		}
		m := chartMode{}
		// An empty chart has no scale to label.
		if peak > 0 {
			m.Top = niceTop(peak)
		}
		// A running total sits at its span's end; an amount in its middle.
		px := func(p int) float64 {
			if cumulative {
				return x(float64(end(p)))
			}
			return x(float64(from(p)+end(p)) / 2)
		}
		for i, s := range all {
			var d strings.Builder
			for p, v := range values[i] {
				cmd := "L"
				if p == 0 {
					cmd = "M"
				}
				y := chartHeight - float64(v)/float64(max(m.Top, 1))*(chartHeight-4) - 1
				fmt.Fprintf(&d, "%s%.1f,%.1f", cmd, px(p)*chartWidth/100, y)
			}
			m.Lines = append(m.Lines, chartLine{Path: d.String(), Slot: slotFor(s.rank), Dashed: s.third})
		}
		for p := range lastPoint + 1 {
			pt := chartPoint{X: x(float64(from(p))), W: x(float64(end(p) - from(p))), At: at(end(p))}
			// An amount names its span, when it covers more than a bin.
			if !cumulative {
				pt.At = at(from(p))
				if until := at(end(p)); until.Sub(pt.At) > binDur {
					pt.Until = &until
				}
			}
			for i, s := range all {
				if v := values[i][p]; v > 0 {
					name, via, _ := modelName(s.model, s.gateway)
					if via != "" {
						name += " (" + via + ")"
					}
					pt.Lines = append(pt.Lines, compactTokens(v)+"  "+name)
				}
			}
			m.Points = append(m.Points, pt)
		}
		return m
	}

	c := &chartView{Amount: mode(amounts, false), Cumulative: mode(totals, true), TickFormat: "clock", TimeFormat: "clock"}
	if tl.Bins*tl.BinMinutes > 1440 {
		c.TickFormat, c.TimeFormat = "day", "dayclock"
	}
	span := time.Duration(tl.Bins) * binDur
	for _, r := range tl.Resets {
		c.Resets = append(c.Resets, chartMark{X: float64(r.Sub(start)) / float64(span) * 100, At: r})
	}
	c.Ticks = []time.Time{start, start.Add(span / 2), start.Add(span)}
	return c
}

// niceTop is a clean top for the axis: 1, 2 or 5 times a power of ten.
func niceTop(peak int64) int64 {
	if peak <= 0 {
		return 1
	}
	step := int64(math.Pow(10, math.Floor(math.Log10(float64(peak)))))
	for _, k := range []int64{1, 2, 5, 10} {
		if k*step >= peak {
			return k * step
		}
	}
	return 10 * step
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
