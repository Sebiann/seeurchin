package httpapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/enderu/seeurchin/internal/poll"
)

// RunTimerSweeper advances polls whose round timers have expired, broadcasting
// each change so connected clients refetch, and firing winner auto-requests for
// any that close. It runs until ctx is cancelled and must be started once per
// process (the in-memory Hub isn't multi-instance safe).
func (s *Server) RunTimerSweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			changed, err := s.svc.SweepDueTimers(ctx, now)
			if err != nil {
				log.Printf("timer sweep: %v", err)
			}
			for _, p := range changed {
				s.broadcast(p.ID, "status")
				if p.Status == poll.StatusClosed {
					s.autoRequestWinners(ctx, p)
				}
			}
		}
	}
}

func (s *Server) handleTimerStart(w http.ResponseWriter, r *http.Request) {
	s.timerAction(w, r, func(p *poll.Poll, me *poll.Participant) (*poll.Poll, error) {
		return s.svc.StartTimer(r.Context(), p, me)
	})
}

func (s *Server) handleTimerPause(w http.ResponseWriter, r *http.Request) {
	s.timerAction(w, r, func(p *poll.Poll, me *poll.Participant) (*poll.Poll, error) {
		return s.svc.PauseTimer(r.Context(), p, me)
	})
}

type extendTimerReq struct {
	AddSeconds int `json:"add_seconds"`
}

func (s *Server) handleTimerExtend(w http.ResponseWriter, r *http.Request) {
	var req extendTimerReq
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	s.timerAction(w, r, func(p *poll.Poll, me *poll.Participant) (*poll.Poll, error) {
		return s.svc.ExtendTimer(r.Context(), p, me, req.AddSeconds)
	})
}

type settingsReq struct {
	VotingConfig json.RawMessage `json:"voting_config"`
	ResultsLive  bool            `json:"results_live"`
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsReq
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, err)
		return
	}
	s.timerAction(w, r, func(p *poll.Poll, me *poll.Participant) (*poll.Poll, error) {
		return p, s.svc.UpdateSettings(r.Context(), p, me, req.VotingConfig, req.ResultsLive)
	})
}

// timerAction loads the poll, runs a host mutation, then broadcasts and
// responds with the refreshed view — the shared shape of the timer and
// settings endpoints.
func (s *Server) timerAction(w http.ResponseWriter, r *http.Request, fn func(*poll.Poll, *poll.Participant) (*poll.Poll, error)) {
	p, err := s.pollFromCode(r)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	me, ok := s.requireParticipant(w, r, p)
	if !ok {
		return
	}
	if _, err := fn(p, me); err != nil {
		s.writeErr(w, err)
		return
	}
	s.broadcast(p.ID, "status")
	s.respondView(w, r, p, me)
}
