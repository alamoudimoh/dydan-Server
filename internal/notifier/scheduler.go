package notifier

import (
	"context"
	"log"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	chRepo "github.com/alamoudimoh/daydan-server/internal/chore/repo"
	"github.com/alamoudimoh/daydan-server/internal/events"
	nRepo "github.com/alamoudimoh/daydan-server/internal/notifier/repo"
	nService "github.com/alamoudimoh/daydan-server/internal/notifier/service"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/alamoudimoh/daydan-server/logging"
)

type keyType string

const (
	SchedulerKey keyType = "scheduler"
)

type Scheduler struct {
	choreRepo        *chRepo.ChoreRepository
	userRepo         *uRepo.UserRepository
	stopChan         chan bool
	notifier         *Notifier
	eventsProducer   *events.EventsProducer
	notificationRepo *nRepo.NotificationRepository
	inbox            *nService.InboxService
	SchedulerJobs    config.SchedulerConfig
}

func NewScheduler(cfg *config.Config, ur *uRepo.UserRepository, cr *chRepo.ChoreRepository, n *Notifier, nr *nRepo.NotificationRepository, inbox *nService.InboxService, ep *events.EventsProducer) *Scheduler {
	return &Scheduler{
		choreRepo:        cr,
		userRepo:         ur,
		stopChan:         make(chan bool),
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
	go s.runScheduler(c, " NOTIFICATION_SCHEDULER ", s.loadAndSendNotificationJob, 3*time.Minute)
	go s.runScheduler(c, " NOTIFICATION_CLEANUP ", s.cleanupSentNotifications, 24*time.Hour*30)
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
func (s *Scheduler) runScheduler(c context.Context, jobName string, job func(c context.Context) (time.Duration, error), interval time.Duration) {

	for {
		logging.FromContext(c).Debug("Scheduler running ", jobName, " time", time.Now().UTC().String())

		select {
		case <-s.stopChan:
			log.Println("Scheduler stopped")
			return
		default:
			elapsedTime, err := job(c)
			if err != nil {
				logging.FromContext(c).Error("Error running scheduler job", err)
			}
			logging.FromContext(c).Debug("Scheduler job completed", jobName, " time: ", elapsedTime.String())
		}
		time.Sleep(interval)
	}
}

func (s *Scheduler) Stop() {
	s.stopChan <- true
}
