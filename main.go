package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	docs "github.com/alamoudimoh/daydan-server/docs"
	"github.com/alamoudimoh/daydan-server/external/payment"
	"github.com/alamoudimoh/daydan-server/frontend"
	auth "github.com/alamoudimoh/daydan-server/internal/auth"
	"github.com/alamoudimoh/daydan-server/internal/auth/apple"
	"github.com/alamoudimoh/daydan-server/internal/chore"
	chRepo "github.com/alamoudimoh/daydan-server/internal/chore/repo"
	"github.com/alamoudimoh/daydan-server/internal/circle"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	"github.com/alamoudimoh/daydan-server/internal/database"
	"github.com/alamoudimoh/daydan-server/internal/device"
	dRepo "github.com/alamoudimoh/daydan-server/internal/device/repo"
	"github.com/alamoudimoh/daydan-server/internal/email"
	"github.com/alamoudimoh/daydan-server/internal/events"
	label "github.com/alamoudimoh/daydan-server/internal/label"
	lRepo "github.com/alamoudimoh/daydan-server/internal/label/repo"
	"github.com/alamoudimoh/daydan-server/internal/maintenance"
	"github.com/alamoudimoh/daydan-server/internal/meter"
	mRepo "github.com/alamoudimoh/daydan-server/internal/meter/repo"
	"github.com/alamoudimoh/daydan-server/internal/mfa"
	"github.com/alamoudimoh/daydan-server/internal/progress"
	"github.com/alamoudimoh/daydan-server/internal/project"
	pjRepo "github.com/alamoudimoh/daydan-server/internal/project/repo"

	sRepo "github.com/alamoudimoh/daydan-server/external/payment/repo"
	sService "github.com/alamoudimoh/daydan-server/external/payment/service"
	notifier "github.com/alamoudimoh/daydan-server/internal/notifier"
	nRepo "github.com/alamoudimoh/daydan-server/internal/notifier/repo"
	nps "github.com/alamoudimoh/daydan-server/internal/notifier/service"
	discord "github.com/alamoudimoh/daydan-server/internal/notifier/service/discord"
	"github.com/alamoudimoh/daydan-server/internal/notifier/service/fcm"
	"github.com/alamoudimoh/daydan-server/internal/notifier/service/pushover"
	telegram "github.com/alamoudimoh/daydan-server/internal/notifier/service/telegram"
	pRepo "github.com/alamoudimoh/daydan-server/internal/points/repo"
	"github.com/alamoudimoh/daydan-server/internal/realtime"
	"github.com/alamoudimoh/daydan-server/internal/resource"
	"github.com/alamoudimoh/daydan-server/internal/reward"
	"github.com/alamoudimoh/daydan-server/internal/routine"
	"github.com/alamoudimoh/daydan-server/internal/shoppinglist"
	slRepo "github.com/alamoudimoh/daydan-server/internal/shoppinglist/repo"
	"github.com/alamoudimoh/daydan-server/internal/storage"
	storageRepo "github.com/alamoudimoh/daydan-server/internal/storage/repo"
	spRepo "github.com/alamoudimoh/daydan-server/internal/subtask/repo"
	dsync "github.com/alamoudimoh/daydan-server/internal/sync"
	"github.com/alamoudimoh/daydan-server/internal/thing"
	tRepo "github.com/alamoudimoh/daydan-server/internal/thing/repo"
	"github.com/alamoudimoh/daydan-server/internal/user"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/alamoudimoh/daydan-server/internal/utils"
	"github.com/alamoudimoh/daydan-server/logging"
	"github.com/alamoudimoh/daydan-server/migrations"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
	"gorm.io/gorm"

	"github.com/alamoudimoh/daydan-server/internal/filter"
	fRepo "github.com/alamoudimoh/daydan-server/internal/filter/repo"
)

func main() {
	// Load configuration first
	cfg := config.LoadConfig()

	// Configure logging from application config
	logging.SetConfigFromAppConfig(
		cfg.Logging.Level,
		cfg.Logging.Encoding,
		cfg.Logging.Development,
	)
	app := fx.New(
		// Startup runs DB migrations synchronously in an OnStart hook. On large
		// datasets the backfill migrations take well over fx's 15s default,
		// which would abort startup and crash-loop. Give them room to finish.
		fx.StartTimeout(15*time.Minute),
		fx.Supply(cfg),
		fx.Supply(logging.DefaultLogger().Desugar()),

		// fx.Provide(config.NewConfig),
		fx.Provide(auth.NewAuthMiddleware),
		fx.Provide(auth.APITokenMiddleware),
		fx.Provide(auth.NewMultiAuthMiddleware),
		fx.Provide(auth.NewIdentityProvider),
		fx.Provide(resource.NewHandler),
		fx.Provide(reward.NewRepository),
		fx.Provide(reward.NewHandler),

		// fx.Provide(NewBot),
		fx.Provide(database.NewDatabase),
		fx.Provide(chRepo.NewChoreRepository),
		fx.Provide(chore.NewHandler),
		fx.Provide(uRepo.NewUserRepository),
		fx.Provide(user.NewDeletionService),
		fx.Provide(user.NewHandler),
		fx.Provide(cRepo.NewCircleRepository),
		fx.Provide(circle.NewHandler),

		// Device management:
		fx.Provide(dRepo.NewDeviceRepository),
		fx.Provide(device.NewHandler),

		fx.Provide(nRepo.NewNotificationRepository),
		fx.Provide(nRepo.NewInboxRepository),
		fx.Provide(notifier.NewInboxHandler),
		fx.Provide(nps.NewNotificationPlanner),
		fx.Provide(nps.NewInboxService),

		// add notifier
		fx.Provide(pushover.NewPushover),
		fx.Provide(telegram.NewTelegramNotifier),
		fx.Provide(discord.NewDiscordNotifier),
		fx.Provide(notifier.NewNotifier),
		fx.Provide(events.NewEventsProducer),
		fx.Provide(fcm.NewFCMNotifier),

		// Rate limiter
		fx.Provide(utils.NewRateLimiter),

		// add email sender (implementation selected by email.provider config):
		fx.Provide(email.NewSender),

		// MFA services
		fx.Provide(mfa.NewService),
		fx.Provide(mfa.NewCleanupService),

		// Auth services
		fx.Provide(auth.NewTokenService),
		fx.Provide(auth.NewCleanupService),

		fx.Provide(apple.NewAppleService),

		// add handlers also
		fx.Provide(newServer),
		fx.Provide(notifier.NewScheduler),

		// things
		fx.Provide(tRepo.NewThingRepository),
		fx.Provide(mRepo.NewRepository),
		fx.Provide(meter.NewHandler),
		fx.Provide(maintenance.NewHandler),
		fx.Provide(routine.NewHandler),

		// points
		fx.Provide(pRepo.NewPointsRepository),
		fx.Provide(spRepo.NewSubTasksRepository),

		// Labels:
		fx.Provide(lRepo.NewLabelRepository),
		fx.Provide(label.NewHandler),

		// Projects:
		fx.Provide(pjRepo.NewProjectRepository),
		fx.Provide(project.NewHandler),
		fx.Provide(slRepo.NewRepository),
		fx.Provide(shoppinglist.NewHandler),
		fx.Provide(progress.NewHandler),

		// Filters:
		fx.Provide(fRepo.NewFilterRepository),
		fx.Provide(filter.NewHandler),

		fx.Provide(thing.NewAPI),
		fx.Provide(thing.NewHandler),

		// External Only:
		fx.Provide(sService.NewStripeService,
			sRepo.NewStripeDB,
			sRepo.NewRevenueCatDB,
			sRepo.NewSubscriptionDB,
		),
		fx.Provide(payment.NewHandler),
		fx.Provide(payment.NewWebhook),
		fx.Provide(chore.NewAPI),

		// Sync:
		fx.Provide(dsync.NewHandler),

		// Frontend
		fx.Provide(frontend.NewHandler),

		// Docs
		fx.Provide(docs.NewHandler),

		// storage: factory selects local vs S3 based on config.Storage.StorageType.
		// Set storage_type: "local" in config for local dev, leave blank for S3.
		fx.Provide(storage.NewStorage),
		fx.Provide(storage.NewURLSigner),
		fx.Provide(storage.NewHandler),
		fx.Provide(storage.NewDraftCleanupService),
		fx.Provide(storageRepo.NewStorageRepository),

		// backup service
		// fx.Provide(backup.NewService),
		// fx.Provide(backup.NewHandler),

		// Real-time service and components
		fx.Provide(realtime.NewRealTimeService),
		fx.Provide(realtime.NewTicketStore),
		fx.Provide(realtime.NewAuthMiddleware),
		fx.Provide(realtime.NewPollingHandler),

		// MCP server

		// fx.Invoke(RunApp),
		fx.Invoke(
			chore.Routes,
			chore.APIs,
			user.Routes,
			circle.Routes,
			device.Routes,
			thing.Routes,
			thing.APIs,
			meter.Routes,
			maintenance.Routes,
			routine.Routes,
			label.Routes,
			project.Routes,
			shoppinglist.Routes,
			progress.Routes,
			filter.Routes,

			storage.Routes,
			frontend.Routes,
			docs.Routes,
			resource.Routes,
			reward.Routes,
			notifier.InboxRoutes,
			// backup.Routes,
			payment.Routes,
			payment.Webhooks,

			dsync.Routes,
			realtime.Routes, // (router, rtAuthMiddleware, pollingHandler, jwtAuth)

			func(r *gin.Engine) {},
		),
	)

	if err := app.Err(); err != nil {
		log.Fatal(err)
	}

	app.Run()

}

func newServer(lc fx.Lifecycle, cfg *config.Config, db *gorm.DB, notifier *notifier.Scheduler, eventProducer *events.EventsProducer, mfaCleanup *mfa.CleanupService, authCleanup *auth.CleanupService, rts *realtime.RealTimeService, draftCleanup *storage.DraftCleanupService, ticketStore *realtime.TicketStore) *gin.Engine {
	// Set Gin mode based on logging configuration
	if cfg.Logging.Development || strings.ToLower(cfg.Logging.Level) == "debug" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// log when http request is made:

	r := gin.New()
	r.Use(gin.Recovery())

	// Never trust client-supplied X-Forwarded-For: gin's default trusts every
	// proxy, which let anyone spoof c.ClientIP() and rotate identities past the
	// IP-based auth rate limiter. Direct clients are the safe default; reverse
	// proxy deployments can still set trusted proxies via config if needed.
	if err := r.SetTrustedProxies(nil); err != nil {
		log.Printf("failed to configure trusted proxies: %v", err)
	}

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      r,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	corsConfig := cors.DefaultConfig()
	allowOrigins := make(map[string]bool)
	for _, origin := range cfg.Server.CorsAllowOrigins {
		allowOrigins[origin] = true
	}
	corsConfig.AllowOriginFunc = func(origin string) bool {
		return allowOrigins[origin]
	}

	corsConfig.AllowCredentials = true
	// Add all headers that browsers commonly send
	corsConfig.AddAllowHeaders(
		"Authorization",
		"secretkey",
		"Cache-Control",
		"Content-Type",
		"Accept",
		"Sec-Ch-Ua",
		"Sec-Ch-Ua-Mobile",
		"Sec-Ch-Ua-Platform",
		"User-Agent",
		"Referer",
		"X-Impersonate-User-ID",
		"refresh_token",
	)
	// Expose headers that the frontend might need
	corsConfig.AddExposeHeaders("Content-Type")
	r.Use(cors.New(corsConfig))

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if cfg.Database.Migration {
				if err := database.Migration(db); err != nil {
					return fmt.Errorf("automatic schema migration: %w", err)
				}
				if err := migrations.Run(context.Background(), db); err != nil {
					return fmt.Errorf("automatic data migration: %w", err)
				}
				if err := database.MigrationScripts(db, cfg); err != nil {
					return fmt.Errorf("automatic SQL migration: %w", err)
				}
			}
			notifier.Start(context.Background())
			eventProducer.Start(context.Background())
			mfaCleanup.Start(context.Background())
			authCleanup.Start(context.Background())
			draftCleanup.Start(context.Background())

			// Start real-time service
			if err := rts.Start(ctx); err != nil {
				log.Printf("Failed to start real-time service: %v", err)
			}

			go func() {
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Fatalf("listen: %s\n", err)
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			// Stop real-time service first with timeout
			done := make(chan error, 1)
			go func() {
				done <- rts.Stop()
			}()

			select {
			case err := <-done:
				if err != nil {
					log.Printf("Failed to stop real-time service: %v", err)
				}
			case <-time.After(5 * time.Second):
				log.Printf("Real-time service shutdown timeout, forcing shutdown")
			}

			mfaCleanup.Stop()
			authCleanup.Stop()
			draftCleanup.Stop()

			// Stop the SSE ticket store cleanup goroutine
			ticketStore.Stop()

			// Shutdown HTTP server with timeout
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := srv.Shutdown(shutdownCtx); err != nil {
				log.Printf("Server shutdown timeout: %s", err)
				// Force close
				srv.Close()
			}

			// Close the database pool last so in-flight writes finish first.
			if sqlDB, err := db.DB(); err == nil {
				if err := sqlDB.Close(); err != nil {
					log.Printf("failed to close database cleanly: %v", err)
				}
			}
			return nil
		},
	})

	// Health-check endpoint: verifies process liveness AND that the database
	// answers queries, so orchestrators restart an instance whose DB is wedged
	// instead of serving requests that can only fail.
	r.GET("/api/v1/health", func(c *gin.Context) {
		pingCtx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		sqlDB, err := db.DB()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "error": "database unavailable"})
			return
		}
		if err := sqlDB.PingContext(pingCtx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "error": "database unreachable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	return r
}
