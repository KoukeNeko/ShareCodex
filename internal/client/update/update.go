// Package update checks GitHub for a newer release. It only reports one;
// the user downloads and installs it themselves.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const latestURL = "https://api.github.com/repos/KoukeNeko/ShareCodex/releases/latest"

type Release struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// Latest returns the newest release when it is newer than current. A dev
// build never reports updates.
func Latest(ctx context.Context, current string) (*Release, error) {
	if current == "dev" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub returned %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if !Newer(body.TagName, current) {
		return nil, nil
	}
	return &Release{Version: strings.TrimPrefix(body.TagName, "v"), URL: body.HTMLURL}, nil
}

// Newer compares dotted numeric versions, ignoring a leading "v" and any
// pre-release suffix.
func Newer(candidate, current string) bool {
	a, b := parts(candidate), parts(current)
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func parts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "-")
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
