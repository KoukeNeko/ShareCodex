package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/transcript"
	"github.com/KoukeNeko/ShareCodex/internal/provider/openai/codex/rollout"
	"github.com/KoukeNeko/ShareCodex/internal/scan"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

type dailyRow struct {
	Product string       `json:"product"`
	Date    string       `json:"date"`
	Model   string       `json:"model"`
	Tokens  usage.Tokens `json:"tokens"`
	Events  int          `json:"events"`
}

// runScan parses every local session log and prints daily totals. It does
// not touch the local database, so it is safe for comparing against other
// tools such as ccusage.
func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	events, err := parseAll()
	if err != nil {
		return err
	}

	rows := map[[3]string]*dailyRow{}
	for _, e := range events {
		k := [3]string{string(e.Product), e.OccurredAt.Local().Format("2006-01-02"), e.Model}
		r, ok := rows[k]
		if !ok {
			r = &dailyRow{Product: k[0], Date: k[1], Model: k[2]}
			rows[k] = r
		}
		r.Tokens = r.Tokens.Add(e.Tokens)
		r.Events++
	}
	out := make([]dailyRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.Product != b.Product {
			return a.Product < b.Product
		}
		return a.Model < b.Model
	})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "date\tproduct\tmodel\tinput\tcached\tcache write\toutput\trequests\t")
	for _, r := range out {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t\n", r.Date, r.Product, r.Model,
			r.Tokens.Input, r.Tokens.CachedInput, r.Tokens.CacheWrite, r.Tokens.Output, r.Events)
	}
	return tw.Flush()
}

func parseAll() ([]usage.Event, error) {
	var events []usage.Event

	codexFiles, err := scan.List(rollout.Roots())
	if err != nil {
		return nil, err
	}
	for path := range codexFiles {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		res, err := rollout.Parse(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		events = append(events, res.Events...)
	}

	claudeFiles, err := scan.List(transcript.Roots())
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	for path := range claudeFiles {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		res, err := transcript.Parse(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		// Resumed sessions copy earlier messages into a new transcript file.
		for _, e := range res.Events {
			i, ok := index[e.DedupeKey]
			if !ok {
				index[e.DedupeKey] = len(events)
				events = append(events, e)
				continue
			}
			events[i].Tokens.Output = max(events[i].Tokens.Output, e.Tokens.Output)
		}
	}
	return events, nil
}
