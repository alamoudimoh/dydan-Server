package notifier

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	chRepo "github.com/alamoudimoh/dydan-server/internal/chore/repo"
	"github.com/alamoudimoh/dydan-server/internal/events"
	nRepo "github.com/alamoudimoh/dydan-server/internal/notifier/repo"
	nService "github.com/alamoudimoh/dydan-server/internal/notifier/service"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/alamoudimoh/dydan-server/logging"
)

type keyType string

const (
	SchedulerKey keyType = "scheduler"
)

type Scheduler struct {
	choreRepo        *chRepo.ChoreRepository
	userRepo         *uRepo.UserRepository
	notifier         *Notifier
	eventsProducer   *events.EventsProducer
	notificationRepo *nRepo.NotificationRepository
	inbox            *nService.InboxService
	SchedulerJobs    config.SchedulerConfig

	// Lifecycle: cancel stops both scheduler loops; stopped closes once all
	// of them have exited so Stop can wait for a bounded graceful shutdown.
	cancel  context.CancelFunc
	stopped chan struct{}
	wg      sync.WaitGroup
}

func NewScheduler(cfg *config.Config, ur *uRepo.UserRepository, cr *chRepo.ChoreRepository, n *Notifier, nr *nRepo.NotificationRepository, inbox *nService.InboxService, ep *events.EventsProducer) *Scheduler {
	return &Scheduler{
		choreRepo:        cr,
		userRepo:         ur,
		notifier:         n,
		notificationRepo: nr,
		inbox:            inbox,
		eventsProducer:   ep,
		SchedulerJobs:    cfg.SchedulerJobs,
	}
}

func (s *Scheduler) Start(c context.Context) {
	log := logging.FromContext(c)
	log.Debug("Scheduler started")
	ctx, cancel := context.WithCancel(c)
	s.cancel = cancel
	s.stopped = make(chan struct{})
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.runScheduler(ctx, " NOTIFICATION_SCHEDULER ", s.loadAndSendNotificationJob, 3*time.Minute)
	}()
	go func() {
		defer s.wg.Done()
		s.runScheduler(ctx, " NOTIFICATION_CLEANUP ", s.cleanupSentNotifications, 24*time.Hour*30)
	}()
	go func() {
		s.wg.Wait()
		close(s.stopped)
	}()
}
func (s *Scheduler) cleanupSentNotifications(c context.Context) (time.Duration, error) {
	log := logging.FromContext(c)
	deleteBefore := time.Now().UTC().Add(-time.Hour * 24 * 30)
	err := s.notificationRepo.DeleteSentNotifications(c, deleteBefore)
	if err != nil {
		log.Error("Error deleting sent notifications", err)
		return time.Duration(0), err
	}
	return time.Duration(0), nil
}

func (s *Scheduler) loadAndSendNotificationJob(c context.Context) (time.Duration, error) {
	log := logging.FromContext(c)
	startTime := time.Now().UTC()
	if err := s.inbox.GenerateScheduled(c, startTime); err != nil {
		log.Error("Error generating in-app notifications", "error", err)
	}
	getAllPendingNotifications, err := s.notificationRepo.GetPendingNotification(c, time.Minute*900)
	log.Debug("Getting pending notifications", " count ", len(getAllPendingNotifications))

	if err != nil {
		log.Error("Error getting pending notifications")
		return time.Since(startTime), err
	}

	for _, notification := range getAllPendingNotifications {
		allowed, preferenceErr := s.inbox.AllowsExternalTaskDelivery(c, notification.UserID, notification.TypeID, startTime)
		if preferenceErr != nil {
			log.Error("Error checking notification delivery preferences", "error", preferenceErr)
			continue
		}
		if !allowed {
			// This is a user-requested suppression, not a retryable transport
			// error. Mark it handled so it cannot be sent later after quiet hours
			// or a category/channel preference changes.
			notification.IsSent = true
			continue
		}
		err := s.notifier.SendNotification(c, notification)
		if err != nil {
			log.Error("Error sending notification", err)
			continue
		}
		if notification.RawEvent != nil && notification.WebhookURL != nil {
			// if we have a webhook url, we should send the event to the webhook
			s.eventsProducer.NotificationEvent(c, *notification.WebhookURL, notification.RawEvent)
		}

		notification.IsSent = true
	}

	s.notificationRepo.MarkNotificationsAsSent(getAllPendingNotifications)
	return time.Since(startTime), nil
}

// runScheduler runs `job` sequentially every `interval`, measured from the
// start of each run (a run longer than the interval never overlaps itself —
// the next tick simply fires immediately). Cancellation is honored between
// and during ticks: ctx.Done replaces the old non-cancellable time.Sleep, so
// shutdown can no longer strand these goroutines for up to a full interval.
func (s *Scheduler) runScheduler(c context.Context, jobName string, job func(c context.Context) (time.Duration, error), interval time.Duration) {
	log := logging.FromContext(c)
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-c.Done():
			return
		case <-timer.C:
		}

		started := time.Now().UTC()
		if err := s.runOnce(c, jobName, job); err != nil {
			log.Errorw("Error running scheduler job", "job", jobName, "error", err)
		}

		delay := interval - time.Since(started)
		if delay < 0 {
			delay = 0
		}
		timer.Reset(delay)
	}
}

// runOnce executes one scheduler tick with panic isolation: a panicking job
// must not kill the loop or the sibling scheduler.
func (s *Scheduler) runOnce(c context.Context, jobName string, job func(c context.Context) (time.Duration, error)) (err error) {
	defer func() {
		if r := recover(); r != nil {
			logging.FromContext(c).Errorw("Scheduler job panicked", "job", jobName, "panic", r)
			err = fmt.Errorf("scheduler job %s panicked", strings.TrimSpace(jobName))
		}
	}()
	elapsedTime, err := job(c)
	logging.FromContext(c).Debugw("Scheduler job completed", "job", jobName, "elapsed", elapsedTime.String())
	return err
}

// Stop cancels both scheduler loops and waits (bounded) for them to exit, so
// shutdown cannot leak notification workers. Safe to call multiple times and
// safe when Start was never called.
func (s *Scheduler) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	select {
	case <-s.stopped:
	case <-time.After(3 * time.Second):
		log.Println("notification scheduler shutdown timeout; abandoning wait")
	}
}
