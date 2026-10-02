// Package httpapi serves the client API under /internal/api/v1.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/server/query"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

const (
	maxPairBody = 64 << 10
	maxSyncBody = 32 << 20

	// maxFieldLen bounds every text field of a synced record; real values
	// are identifiers, hashes and masked emails.
	maxFieldLen = 512
	// maxEventTokens bounds each token count of one request, far past any
	// context window, so the sums of counts the server reads cannot overflow.
	maxEventTokens = 1 << 31
)

type Server struct {
	store *storage.Store
	log   *slog.Logger
}

func New(store *storage.Store, log *slog.Logger) http.Handler {
	s := &Server{store: store, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET "+syncapi.PathJoin+"{code}", s.join)
	mux.HandleFunc("POST "+syncapi.PathPair, s.pair)
	mux.HandleFunc("POST "+syncapi.PathSync, s.authed(s.sync))
	mux.HandleFunc("GET "+syncapi.PathOverview, s.authed(s.overview))
	mux.HandleFunc("POST "+syncapi.PathInvite, s.authed(s.invite))
	mux.HandleFunc("POST "+syncapi.PathAccounts+"{id}/leave", s.authed(s.leaveAccount))
	mux.HandleFunc("GET "+syncapi.PathPeople+"{id}/usage", s.authed(s.memberUsage))
	return mux
}

type deviceKey struct{}

func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing device token")
			return
		}
		d, err := s.store.DeviceByToken(r.Context(), token)
		if errors.Is(err, storage.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if err != nil {
			s.internalError(w, "authenticate device", err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), deviceKey{}, d)))
	}
}

func deviceFrom(r *http.Request) storage.Device {
	return r.Context().Value(deviceKey{}).(storage.Device)
}

// join answers a join link opened in a browser; pairing happens in the app.
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "請在 ShareCodex app 貼上這個加入連結，或在 Linux 執行 sharecodex join。")
	fmt.Fprintln(w, "Paste this join link into the ShareCodex app, or run sharecodex join on Linux.")
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var req syncapi.PairRequest
	if !decode(w, r, maxPairBody, &req) {
		return
	}
	if req.Version != 1 && req.Version != syncapi.Version {
		writeError(w, http.StatusUpgradeRequired, "client protocol version is not supported; update ShareCodex")
		return
	}
	name := strings.TrimSpace(req.DeviceName)
	if req.Code == "" || name == "" || len(name) > 100 {
		writeError(w, http.StatusBadRequest, "code and device_name are required")
		return
	}
	d, token, err := s.store.Pair(r.Context(), strings.TrimSpace(req.Code), name, req.Platform)
	if errors.Is(err, storage.ErrInvalidInvite) {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, "pair device", err)
		return
	}
	s.log.Info("device paired", "person", d.Person.DisplayName, "device", d.Name)
	writeJSON(w, http.StatusOK, syncapi.PairResponse{
		DeviceID: d.ID, PersonID: d.Person.ID, PersonName: d.Person.DisplayName, Token: token,
	})
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	var req syncapi.SyncRequest
	if !decode(w, r, maxSyncBody, &req) {
		return
	}
	if req.Version != 1 && req.Version != syncapi.Version {
		writeError(w, http.StatusUpgradeRequired, "client protocol version is not supported; update ShareCodex")
		return
	}
	if req.Version == 1 {
		for i := range req.Events {
			req.Events[i].PreviousAccountRefHash = ""
		}
		for i := range req.Snapshots {
			req.Snapshots[i].PreviousAccountRefHash = ""
		}
	}
	d := deviceFrom(r)
	if n := dropInvalid(&req); n > 0 {
		s.log.Warn("dropped out-of-range sync records", "person", d.Person.DisplayName, "device", d.Name, "records", n)
	}
	res, err := s.store.Ingest(r.Context(), d, req)
	if err != nil {
		s.internalError(w, "ingest batch", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// dropInvalid removes the records a real client cannot produce, with a
// negative or absurd token count or an overlong text field, and returns how
// many it removed. Rejecting the batch instead would leave one bad record
// blocking everything the device has queued behind it.
func dropInvalid(req *syncapi.SyncRequest) int {
	before := len(req.Observations) + len(req.Events) + len(req.Snapshots)
	req.Observations = slices.DeleteFunc(req.Observations, func(o syncapi.Observation) bool {
		return tooLong(o.Provider, o.Source, o.AccountRefHash, o.Hint, o.PlanType)
	})
	req.Events = slices.DeleteFunc(req.Events, func(e syncapi.Event) bool {
		for _, n := range []int64{e.Input, e.CachedInput, e.CacheWrite, e.Output, e.ReasoningOutput} {
			if n < 0 || n > maxEventTokens {
				return true
			}
		}
		return tooLong(e.DedupeKey, e.AccountRefHash, e.PreviousAccountRefHash, e.Provider, e.Product,
			e.Originator, e.SessionID, e.Model, e.Gateway)
	})
	req.Snapshots = slices.DeleteFunc(req.Snapshots, func(sn syncapi.Snapshot) bool {
		return tooLong(sn.AccountRefHash, sn.PreviousAccountRefHash, sn.AccountHint, sn.PlanType, sn.Provider, sn.Source)
	})
	return before - len(req.Observations) - len(req.Events) - len(req.Snapshots)
}

func tooLong(fields ...string) bool {
	return slices.ContainsFunc(fields, func(f string) bool { return len(f) > maxFieldLen })
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	o, err := query.Overview(r.Context(), s.store, deviceFrom(r).PersonID, now, startOfDay(r.URL.Query().Get(syncapi.QueryToday), now))
	if err != nil {
		s.internalError(w, "build overview", err)
		return
	}
	if o.DashboardPublished, err = s.store.PublicDashboard(r.Context()); err != nil {
		s.internalError(w, "read dashboard setting", err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// memberUsage serves any member's usage to any member, as the overview
// already shows each member's share of an account.
func (s *Server) memberUsage(w http.ResponseWriter, r *http.Request) {
	period := query.PeriodByID(r.URL.Query().Get(syncapi.QueryPeriod))
	u, err := query.MemberUsage(r.Context(), s.store, deviceFrom(r).PersonID, r.PathValue("id"), period, time.Now())
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}
	if err != nil {
		s.internalError(w, "build member usage", err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// startOfDay reads the client's start of day, falling back to UTC midnight
// when it is missing or is not within the last day.
func startOfDay(raw string, now time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, raw); err == nil && !t.After(now) && now.Sub(t) <= 24*time.Hour {
		return t
	}
	return now.UTC().Truncate(24 * time.Hour)
}

// invite lets a joined device add another device for the same person, so
// members need not ask an admin for every machine they use.
func (s *Server) invite(w http.ResponseWriter, r *http.Request) {
	d := deviceFrom(r)
	code, expires, err := s.store.CreateInvite(r.Context(), d.PersonID, storage.InviteTTL)
	if err != nil {
		s.internalError(w, "create invite", err)
		return
	}
	s.log.Info("device created invite", "person", d.Person.DisplayName, "device", d.Name)
	writeJSON(w, http.StatusOK, syncapi.InviteResponse{Code: code, ExpiresAt: expires})
}

// leaveAccount takes the member out of an account's allotment and off their
// overview. The membership stays at weight 0, so signing in to the account
// again does not add them back; an admin can restore the weight.
func (s *Server) leaveAccount(w http.ResponseWriter, r *http.Request) {
	d := deviceFrom(r)
	id := r.PathValue("id")
	if _, err := s.store.Account(r.Context(), id); errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	} else if err != nil {
		s.internalError(w, "load account", err)
		return
	}
	if err := s.store.SetShareWeight(r.Context(), id, d.PersonID, 0); err != nil {
		s.internalError(w, "leave account", err)
		return
	}
	s.log.Info("member left account", "person", d.Person.DisplayName, "account", id)
	writeJSON(w, http.StatusOK, struct{}{})
}

func decode(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) internalError(w http.ResponseWriter, action string, err error) {
	s.log.Error(action, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, syncapi.Error{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
