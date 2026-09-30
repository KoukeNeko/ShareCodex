package agent

import (
	"log/slog"
	"os"
	gosync "sync"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/provider/opencodex"
)

// openCodexLog is OpenCodex's request log, read again only when it changes.
type openCodexLog struct {
	mu      gosync.Mutex
	size    int64
	modTime time.Time
	recs    []opencodex.Record
}

// records returns the log's requests; with no log, or one that cannot be
// read, there are none and requests keep their default account.
func (l *openCodexLog) records(log *slog.Logger) []opencodex.Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	path, err := opencodex.Path()
	if err != nil {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		l.recs, l.size = nil, 0
		return nil
	}
	if info.Size() == l.size && info.ModTime().Equal(l.modTime) {
		return l.recs
	}
	recs, err := opencodex.Read(path)
	if err != nil {
		log.Warn("read OpenCodex usage", "err", err)
		return l.recs
	}
	l.recs, l.size, l.modTime = recs, info.Size(), info.ModTime()
	return recs
}
