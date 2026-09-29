package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"
	"github.com/redis/go-redis/v9"
)

const (
	sessionCookieName = "mhq_gm_session"
	csrfCookieName    = "mhq_gm_csrf"
	sessionTTL        = 8 * time.Hour
)

type apiError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type apiEnvelope struct {
	RequestID string    `json:"requestId"`
	Data      any       `json:"data,omitempty"`
	Error     *apiError `json:"error,omitempty"`
}

type sessionData struct {
	ID          string   `json:"id"`
	AdminID     int64    `json:"adminId"`
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	Roles       []string `json:"roles"`
	CSRF        string   `json:"csrf"`
	ExpiresAt   int64    `json:"expiresAt"`
}

type sessionBackend interface {
	Put(context.Context, *sessionData) error
	Get(context.Context, string) (*sessionData, error)
	Delete(context.Context, string) error
}

type redisSessions struct {
	client *redis.Client
}

func (s redisSessions) key(id string) string { return "mhq:gm:session:" + id }

func (s redisSessions) Put(ctx context.Context, session *sessionData) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.key(session.ID), data, sessionTTL).Err()
}

func (s redisSessions) Get(ctx context.Context, id string) (*sessionData, error) {
	value, err := s.client.Get(ctx, s.key(id)).Bytes()
	if err == redis.Nil {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	var session sessionData
	if err := json.Unmarshal(value, &session); err != nil {
		return nil, err
	}
	if session.ExpiresAt <= time.Now().Unix() {
		_ = s.client.Del(ctx, s.key(id)).Err()
		return nil, sql.ErrNoRows
	}
	return &session, nil
}

func (s redisSessions) Delete(ctx context.Context, id string) error {
	return s.client.Del(ctx, s.key(id)).Err()
}

type memorySessions struct {
	mu    sync.RWMutex
	items map[string]*sessionData
}

func newMemorySessions() *memorySessions {
	return &memorySessions{items: make(map[string]*sessionData)}
}

func (s *memorySessions) Put(_ context.Context, session *sessionData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[session.ID] = session
	return nil
}

func (s *memorySessions) Get(_ context.Context, id string) (*sessionData, error) {
	s.mu.RLock()
	value := s.items[id]
	s.mu.RUnlock()
	if value == nil || value.ExpiresAt <= time.Now().Unix() {
		if value != nil {
			_ = s.Delete(context.Background(), id)
		}
		return nil, sql.ErrNoRows
	}
	copyValue := *value
	copyValue.Roles = append([]string(nil), value.Roles...)
	return &copyValue, nil
}

func (s *memorySessions) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
	return nil
}

type App struct {
	db           *sql.DB
	redis        *redis.Client
	sessions     sessionBackend
	gameAddr     string
	cookieSecure bool
	allowMemory  bool
	startedAt    time.Time
	trustedProxy []net.IPNet
}

func newApp(db *sql.DB, client *redis.Client, gameAddr string, allowMemory bool) *App {
	app := &App{
		db: db, redis: client, gameAddr: gameAddr, cookieSecure: envBool("GM_COOKIE_SECURE", false),
		allowMemory: allowMemory, startedAt: time.Now(),
	}
	if client != nil {
		app.sessions = redisSessions{client: client}
	} else if allowMemory {
		app.sessions = newMemorySessions()
	}
	return app
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func requestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

func writeJSON(w http.ResponseWriter, status int, requestID string, data any, apiErr *apiError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiEnvelope{RequestID: requestID, Data: data, Error: apiErr})
}

func writeError(w http.ResponseWriter, status int, requestID, code, message string) {
	writeJSON(w, status, requestID, nil, &apiError{Code: code, Message: message})
}

func (a *App) requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 15*time.Second)
}

func (a *App) sessionFromRequest(r *http.Request) (*sessionData, error) {
	if a.sessions == nil {
		return nil, errors.New("session backend unavailable")
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return nil, sql.ErrNoRows
	}
	ctx, cancel := a.requestContext(r)
	defer cancel()
	return a.sessions.Get(ctx, cookie.Value)
}

func (a *App) newSession(adminID int64, username, displayName string, roles []string) (*sessionData, error) {
	if a.sessions == nil {
		return nil, errors.New("session backend unavailable")
	}
	return &sessionData{ID: requestID(), AdminID: adminID, Username: username, DisplayName: displayName,
		Roles: append([]string(nil), roles...), CSRF: requestID(), ExpiresAt: time.Now().Add(sessionTTL).Unix()}, nil
}

func (a *App) setSessionCookies(w http.ResponseWriter, session *sessionData) {
	secure := a.cookieSecure
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: session.ID, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: session.CSRF, Path: "/", HttpOnly: false,
		Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
}

func (a *App) clearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: name == sessionCookieName,
			Secure: a.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	}
}

func (a *App) checkCSRF(r *http.Request, session *sessionData) bool {
	header := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || header == "" || session == nil {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(header), []byte(session.CSRF)) != 1 ||
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(session.CSRF)) != 1 {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		origin = strings.TrimSpace(r.Header.Get("Referer"))
	}
	if origin != "" {
		parsed, err := neturlParse(origin)
		if err != nil || !sameOrigin(r, parsed) {
			return false
		}
	}
	return true
}

// neturlParse is kept as a tiny wrapper so tests can replace the parsing edge
// without introducing a URL dependency throughout the request code.
func neturlParse(raw string) (*urlValue, error) {
	value, err := parseURL(raw)
	if err != nil {
		return nil, err
	}
	return &urlValue{scheme: value.scheme, host: value.host}, nil
}

type urlValue struct{ scheme, host string }

func sameOrigin(r *http.Request, value *urlValue) bool {
	if value == nil || value.host == "" {
		return false
	}
	host := r.Host
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	return strings.EqualFold(value.host, host) && (value.scheme == "http" || value.scheme == "https")
}

func parseURL(raw string) (*urlValue, error) {
	parts := strings.SplitN(strings.TrimSpace(raw), "://", 2)
	if len(parts) != 2 || (parts[0] != "http" && parts[0] != "https") {
		return nil, errors.New("invalid origin")
	}
	host := strings.Split(parts[1], "/")[0]
	if host == "" || strings.ContainsAny(host, "?#") {
		return nil, errors.New("invalid origin host")
	}
	return &urlValue{scheme: parts[0], host: host}, nil
}

func parseInt64Param(raw string, fallback int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return fallback
	}
	return value
}

func clampLimit(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > 100 {
		return 100
	}
	return value
}

func validateReason(reason string) error {
	reason = strings.TrimSpace(reason)
	if len(reason) < 4 {
		return errors.New("reason must be at least 4 characters")
	}
	if len(reason) > 500 {
		return errors.New("reason is too long")
	}
	return nil
}

func logStartup(app *App) {
	if app.allowMemory && app.redis == nil {
		log.Print("WARNING: GM memory sessions enabled; use Redis sessions outside local development")
	}
}
