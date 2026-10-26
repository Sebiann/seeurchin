// Package httpapi exposes the REST + SSE API and serves the embedded SvelteKit
// frontend.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/enderu/seeurchin/internal/auth"
	"github.com/enderu/seeurchin/internal/codes"
	"github.com/enderu/seeurchin/internal/config"
	"github.com/enderu/seeurchin/internal/jellyfin"
	"github.com/enderu/seeurchin/internal/poll"
	"github.com/enderu/seeurchin/internal/seerr"
)

const cookieName = "seeurchin_session"

// Server wires the HTTP handlers to their dependencies.
type Server struct {
	cfg          config.Config
	svc          *poll.Service
	repo         poll.Repository
	jf           *jellyfin.Client
	seerr        *seerr.Client // nil when Seerr is not configured
	sessions     *auth.Sessions
	hub          *Hub
	loginLimiter *loginLimiter
}

// NewServer constructs a Server. sr may be nil, which disables write-in /
// Seerr-request features.
func NewServer(cfg config.Config, svc *poll.Service, repo poll.Repository, jf *jellyfin.Client, sr *seerr.Client, sessions *auth.Sessions, hub *Hub) *Server {
	return &Server{cfg: cfg, svc: svc, repo: repo, jf: jf, seerr: sr, sessions: sessions, hub: hub, loginLimiter: newLoginLimiter()}
}

// seerrEnabled reports whether Seerr-backed features (write-ins, auto-request)
// are available.
func (s *Server) seerrEnabled() bool { return s.seerr != nil }

// Routes returns the configured HTTP handler.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)
		r.Get("/features", s.handleFeatures)
		r.Get("/methods", s.handleMethods)
		r.Get("/genres", s.handleGenres)

		// Jellyfin user login. Identity gates the NAS-touching actions (poll
		// creation, write-in request) and authorizes admin access. When
		// SEEURCHIN_ENABLE_USER_LOGIN is off, requireUser is a no-op.
		r.Get("/user/session", s.handleUserSession)
		r.Post("/user/login", s.handleUserLogin)
		r.Post("/user/logout", s.handleUserLogout)

		r.With(s.requireUser).Post("/polls", s.handleCreatePoll)
		r.Route("/polls/{code}", func(r chi.Router) {
			r.Get("/", s.handleGetPoll)
			r.Post("/join", s.handleJoin)
			r.Get("/library", s.handleLibrary)
			r.Get("/search-external", s.handleSearchExternal)
			r.Post("/nominations", s.handleNominate)
			r.Delete("/nominations/{id}", s.handleWithdraw)
			r.Post("/advance", s.handleAdvance)
			r.Post("/merge-duplicates", s.handleMergeDuplicates)
			r.Post("/link", s.handleLink)
			r.Get("/jellyfin-users", s.handleJellyfinUsers)
			r.Post("/participants/{id}/link", s.handleHostLink)
			r.Post("/participants/{id}/merge", s.handleMergePerson)
			r.Post("/tiebreak", s.handleBreakTie)
			r.Post("/timer/start", s.handleTimerStart)
			r.Post("/timer/pause", s.handleTimerPause)
			r.Post("/timer/extend", s.handleTimerExtend)
			r.Patch("/settings", s.handleUpdateSettings)
			r.Post("/votes", s.handleVote)
			r.With(s.requireUser).Post("/request/{id}", s.handleRequestWinner)
			r.Get("/results", s.handleResults)
			r.Get("/events", s.handleEvents)
		})
		r.Get("/items/{id}/image", s.handleImage)

		// Admin dashboard (poll history + management). Sign-in is the shared
		// Jellyfin login (/api/user/*); admin is an authorization check on that
		// identity. handleAdminSession 404s when the dashboard isn't configured;
		// the data endpoints are gated by requireAdmin (404 disabled, 401 not
		// signed in, 403 not an admin account).
		r.Route("/admin", func(r chi.Router) {
			r.Get("/session", s.handleAdminSession)
			r.Group(func(r chi.Router) {
				r.Use(s.requireAdmin)
				r.Get("/polls", s.handleAdminPolls)
				r.Get("/polls/{code}", s.handleAdminPoll)
				r.Get("/polls/{code}/ballots", s.handleAdminBallots)
				r.Delete("/polls/{code}", s.handleAdminDeletePoll)
				r.Post("/polls/{code}/advance", s.handleAdminAdvance)
			})
		})

		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			s.writeJSON(w, http.StatusNotFound, errResp{"not found"})
		})
	})

	// Poll routes get server-rendered Open Graph tags + a generated share card
	// so links unfurl with a branded preview in chat apps. Registered before the
	// SPA catch-all; real browsers still boot the SPA from the injected page.
	r.Get("/p/{code}", s.handlePollPage)
	r.Get("/p/{code}/preview.png", s.handlePollPreview)

	// Everything else is the SPA / static assets.
	r.Handle("/*", s.spaHandler())
	return r
}

// --- request/response helpers ---

type errResp struct {
	Error string `json:"error"`
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (s *Server) writeErr(w http.ResponseWriter, err error) {
	var pe *poll.Error
	switch {
	case errors.As(err, &pe):
		s.writeJSON(w, pe.Code, errResp{pe.Msg})
	case poll.IsNotFound(err):
		s.writeJSON(w, http.StatusNotFound, errResp{"not found"})
	default:
		log.Printf("internal error: %v", err)
		s.writeJSON(w, http.StatusInternalServerError, errResp{"internal error"})
	}
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &poll.Error{Code: http.StatusBadRequest, Msg: "invalid request body"}
	}
	return nil
}

// pollFromCode loads the poll named by the {code} URL parameter, normalizing
// user-entered codes.
func (s *Server) pollFromCode(r *http.Request) (*poll.Poll, error) {
	code := codes.Normalize(chi.URLParam(r, "code"))
	return s.repo.GetPollByCode(r.Context(), code)
}

// --- session cookie ---

func (s *Server) readTokens(r *http.Request) map[string]string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return map[string]string{}
	}
	tokens, err := s.sessions.Decode(c.Value)
	if err != nil {
		return map[string]string{}
	}
	return tokens
}

func (s *Server) setToken(w http.ResponseWriter, r *http.Request, pollID, token string) {
	tokens := s.readTokens(r)
	tokens[pollID] = token
	val, err := s.sessions.Encode(tokens)
	if err != nil {
		log.Printf("encode session: %v", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    val,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(s.cfg.BaseURL, "https"),
		MaxAge:   60 * 60 * 24 * 30,
	})
}

// currentParticipant resolves the participant for poll p from the session
// cookie, or nil if the caller has not joined.
func (s *Server) currentParticipant(r *http.Request, p *poll.Poll) *poll.Participant {
	token := s.readTokens(r)[p.ID]
	if token == "" {
		return nil
	}
	part, err := s.repo.GetParticipantBySession(r.Context(), p.ID, token)
	if err != nil {
		return nil
	}
	return part
}
