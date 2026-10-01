// Package antigravity reads Google Antigravity's CLI (agy): the Google
// account it is signed into, and the token usage it records for each model
// request in its conversation databases. Only counts, model names and times
// are read; prompts and responses are never touched.
//
// The databases are SQLite files under ~/.gemini/antigravity-cli/
// conversations. Each request's usage is a ModelUsageStats protobuf in the
// gen_metadata table; the step it answered, in the steps table, carries the
// time. Field numbers below come from the descriptors built into agy.
package antigravity

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	_ "modernc.org/sqlite"

	"github.com/KoukeNeko/ShareCodex/internal/account"
	"github.com/KoukeNeko/ShareCodex/internal/provider"
	"github.com/KoukeNeko/ShareCodex/internal/scan"
	"github.com/KoukeNeko/ShareCodex/internal/usage"
)

// geminiDir is ~/.gemini, which agy shares with the Gemini CLI.
func geminiDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini"), nil
}

// Roots is where agy keeps its conversation databases.
func Roots() []string {
	dir, err := geminiDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(dir, "antigravity-cli", "conversations")}
}

// List returns every conversation database under roots. A database is
// written through its -wal file until a checkpoint, so the file's state
// includes the WAL's: a new request changes it even when the database file
// itself does not.
func List(roots []string) (map[string]scan.FileState, error) {
	files := map[string]scan.FileState{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
				continue
			}
			path := filepath.Join(root, e.Name())
			info, err := os.Stat(path)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			st := scan.FileState{Size: info.Size(), ModTime: info.ModTime()}
			if wal, err := os.Stat(path + "-wal"); err == nil {
				st.Size += wal.Size()
				if wal.ModTime().After(st.ModTime) {
					st.ModTime = wal.ModTime()
				}
			}
			files[path] = st
		}
	}
	return files, nil
}

// ModelUsageStats field numbers.
const (
	usageInputTokens    = 2
	usageOutputTokens   = 3
	usageCacheWrite     = 4
	usageCacheRead      = 5
	usageThinkingTokens = 9
)

// Generation metadata: the response's metadata is field 1; in it, field 4
// is its ModelUsageStats, 19 the model, and 20 key/value pairs, among them
// the index of the step it answered.
const (
	genResponse      = 1
	responseUsage    = 4
	responseModel    = 19
	responseLabels   = 20
	labelKey         = 1
	labelValue       = 2
	stepIndexLabel   = "last_step_index"
	stepMetadataTime = 1 // the step's creation time, a Timestamp
)

// Parse reads one conversation database into usage events, one per model
// request that reported usage.
func Parse(path string) ([]usage.Event, error) {
	// Read-only, as agy may be writing the database; WAL lets both work.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	conversation := strings.TrimSuffix(filepath.Base(path), ".db")
	times, err := stepTimes(db)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT idx, data FROM gen_metadata ORDER BY idx`)
	if err != nil {
		return nil, fmt.Errorf("read generations: %w", err)
	}
	defer rows.Close()
	var events []usage.Event
	for rows.Next() {
		var idx int
		var data []byte
		if err := rows.Scan(&idx, &data); err != nil {
			return nil, err
		}
		g, ok := parseGeneration(data)
		if !ok {
			continue
		}
		at, ok := times[g.step]
		if !ok {
			continue
		}
		events = append(events, usage.Event{
			DedupeKey:  "antigravity:" + conversation + ":" + strconv.Itoa(idx),
			Provider:   account.ProviderGoogle,
			Product:    usage.ProductAntigravity,
			SessionID:  conversation,
			Model:      g.model,
			OccurredAt: at,
			Tokens:     g.tokens,
		})
	}
	return events, rows.Err()
}

type generation struct {
	model  string
	step   int
	tokens usage.Tokens
}

// parseGeneration reads one gen_metadata row; ok is false for a row with no
// model or usage, such as a request that failed.
func parseGeneration(data []byte) (generation, bool) {
	var g generation
	var stats []byte
	step := -1
	walk(data, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num != genResponse || typ != protowire.BytesType {
			return
		}
		walk(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
			if typ != protowire.BytesType {
				return
			}
			switch num {
			case responseUsage:
				stats = v
			case responseModel:
				g.model = string(v)
			case responseLabels:
				var key, value string
				walk(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
					switch {
					case num == labelKey && typ == protowire.BytesType:
						key = string(v)
					case num == labelValue && typ == protowire.BytesType:
						value = string(v)
					}
				})
				if key == stepIndexLabel {
					if n, err := strconv.Atoi(value); err == nil {
						step = n
					}
				}
			}
		})
	})
	if g.model == "" || stats == nil || step < 0 {
		return g, false
	}
	g.step = step
	var input, output, cacheWrite, cacheRead, thinking int64
	walk(stats, func(num protowire.Number, typ protowire.Type, _ []byte, n uint64) {
		if typ != protowire.VarintType {
			return
		}
		switch num {
		case usageInputTokens:
			input = int64(n)
		case usageOutputTokens:
			output = int64(n)
		case usageCacheWrite:
			cacheWrite = int64(n)
		case usageCacheRead:
			cacheRead = int64(n)
		case usageThinkingTokens:
			thinking = int64(n)
		}
	})
	// Input excludes cached input, as the ledger does: a request reading
	// the cache reports the rest of its prompt alone. Output already
	// includes thinking.
	g.tokens = usage.Tokens{
		Input:           input,
		CachedInput:     cacheRead,
		CacheWrite:      cacheWrite,
		Output:          output,
		ReasoningOutput: thinking,
	}
	return g, input+output > 0
}

// stepTimes maps each step's index to when it was created.
func stepTimes(db *sql.DB) (map[int]time.Time, error) {
	rows, err := db.Query(`SELECT idx, metadata FROM steps`)
	if err != nil {
		return nil, fmt.Errorf("read steps: %w", err)
	}
	defer rows.Close()
	times := map[int]time.Time{}
	for rows.Next() {
		var idx int
		var meta []byte
		if err := rows.Scan(&idx, &meta); err != nil {
			return nil, err
		}
		walk(meta, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
			if num != stepMetadataTime || typ != protowire.BytesType {
				return
			}
			var secs, nanos int64
			walk(v, func(num protowire.Number, typ protowire.Type, _ []byte, n uint64) {
				if typ != protowire.VarintType {
					return
				}
				switch num {
				case 1:
					secs = int64(n)
				case 2:
					nanos = int64(n)
				}
			})
			if secs > 0 {
				times[idx] = time.Unix(secs, nanos).UTC()
			}
		})
	}
	return times, rows.Err()
}

// walk calls fn for each field of a protobuf message: v holds a bytes
// field's contents and n a varint's value. It stops at the first field it
// cannot read, which leaves a damaged message partly read rather than
// misread.
func walk(b []byte, fn func(num protowire.Number, typ protowire.Type, v []byte, n uint64)) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		b = b[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return
			}
			fn(num, typ, nil, v)
			b = b[n:]
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return
			}
			fn(num, typ, v, 0)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return
			}
			b = b[n:]
		}
	}
}

// accountsFile lists the Google accounts signed in to agy and the Gemini
// CLI, which share ~/.gemini; Active is the one in use.
type accountsFile struct {
	Active string `json:"active"`
}

// Observe reports the Google account agy is signed into. It returns
// exec.ErrNotFound when agy is not installed.
func Observe() (account.Observation, error) {
	dir, err := geminiDir()
	if err != nil {
		return account.Observation{}, err
	}
	if _, err := provider.LookPath("agy"); err != nil {
		if _, statErr := os.Stat(filepath.Join(dir, "antigravity-cli")); statErr != nil {
			return account.Observation{}, exec.ErrNotFound
		}
	}
	o := account.Observation{Provider: account.ProviderGoogle, ObservedAt: time.Now().UTC()}
	b, err := os.ReadFile(filepath.Join(dir, "google_accounts.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return o, nil
	}
	if err != nil {
		return account.Observation{}, err
	}
	var f accountsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return account.Observation{}, fmt.Errorf("read google_accounts.json: %w", err)
	}
	if email := strings.TrimSpace(f.Active); email != "" {
		o.ExternalRefHash = account.HashExternalRef(account.ProviderGoogle, strings.ToLower(email))
		o.Hint = account.MaskRef(email)
	}
	return o, nil
}
