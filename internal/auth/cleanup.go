package auth

import (
	"context"
	"time"

	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/alamoudimoh/daydan-server/logging"
)

type CleanupService struct {
	userRepo *uRepo.UserRepository
	ticker   *time.Ticker
	done     chan bool
}

func NewCleanupService(userRepo *uRepo.UserRepository) *CleanupService {
	return &CleanupService{
		userRepo: userRepo,
		ticker:   time.NewTicker(60 * time.Minute),
		// Buffered so Stop never blocks while a cleanup job is mid-run.
		done:     make(chan bool, 1),
	}
}

func (s *CleanupService) Start(ctx context.Context) {
	logger := logging.FromContext(ctx)
	logger.Info("Auth session cleanup service started")

	go func() {
		for {
			select {
			case <-s.done:
				logger.Info("Auth session cleanup service stopped")
				return
			case <-s.ticker.C:
				if err := s.userRepo.CleanupExpiredSessions(ctx); err != nil {
					logger.Errorw("Failed to cleanup expired user sessions", "error", err)
				} else {
					logger.Debug("Successfully cleaned up expired user sessions")
				}
				if err := s.userRepo.CleanupExpiredPasswordResets(ctx); err != nil {
					logger.Errorw("Failed to cleanup expired password resets", "error", err)
				}
			}
		}
	}()
}

// Stop stops the cleanup service
func (s *CleanupService) Stop() {
	s.ticker.Stop()
	s.done <- true
}
