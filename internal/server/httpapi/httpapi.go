// Package httpapi serves the client API under /internal/api/v1.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/server/query"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
	"github.com/KoukeNeko/ShareCodex/internal/syncapi"
)

const (
	maxPairBody = 64 << 10
	maxSyncBody = 32 << 20
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
	if req.Version != syncapi.Version {
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
	if req.Version != syncapi.Version {
		writeError(w, http.StatusUpgradeRequired, "client protocol version is not supported; update ShareCodex")
		return
	}
	n, err := s.store.Ingest(r.Context(), deviceFrom(r), req)
	if err != nil {
		s.internalError(w, "ingest batch", err)
		return
	}
	writeJSON(w, http.StatusOK, syncapi.SyncResponse{Accepted: n})
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	o, err := query.Overview(r.Context(), s.store, deviceFrom(r).PersonID, time.Now())
	if err != nil {
		s.internalError(w, "build overview", err)
		return
	}
	writeJSON(w, http.StatusOK, o)
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
