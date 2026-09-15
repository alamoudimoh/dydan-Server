package database

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/plugin/opentelemetry/tracing"

	// "gorm.io/driver/sqlite" // Sqlite driver based on CGO
	"gorm.io/gorm/logger"

	"github.com/alamoudimoh/dydan-server/config"
	"github.com/alamoudimoh/dydan-server/logging"
	"github.com/glebarez/sqlite" // Pure go SQLite driver, checkout https://github.com/glebarez/sqlite for details
	"gorm.io/gorm"
)

// convertLogLevelToGorm converts application log level to GORM log level
func convertLogLevelToGorm(appLogLevel string) logger.LogLevel {
	switch strings.ToLower(appLogLevel) {
	case "debug":
		return logger.Info // GORM's most verbose level for debugging
	case "info":
		return logger.Warn // Show warnings and errors, but not all queries
	case "warn", "warning", "error", "dpanic", "panic", "fatal":
		return logger.Error // Only show errors
	case "silent":
		return logger.Silent // No logging from GORM
	default:
		return logger.Error // Default to error level for production safety
	}
}

// sqliteDSN appends durability pragmas to the SQLite path (unless the caller
// already supplied a DSN). WAL keeps readers from blocking writers, a
// busy_timeout absorbs transient write contention between HTTP handlers and
// background schedulers instead of failing with "database is locked", and
// NORMAL synchronous mode is the recommended pairing with WAL: durable across
// application crashes while avoiding an fsync on every commit.
func sqliteDSN(path string) string {
	if strings.Contains(path, "?") {
		return path
	}
	pragmas := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=synchronous(NORMAL)",
	}
	return path + "?" + strings.Join(pragmas, "&")
}

func NewDatabase(cfg *config.Config) (*gorm.DB, error) {
	var db *gorm.DB
	var err error

	// Use GORM logger level based on application log level from config
	gormLogLevel := convertLogLevelToGorm(cfg.Logging.Level)
	newLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             200 * time.Millisecond, // Default Logger behavior
			LogLevel:                  gormLogLevel,
			IgnoreRecordNotFoundError: false,                        // Default Logger behavior
			Colorful:                  true,                         // Default Logger behavior
			ParameterizedQueries:      cfg.Logging.Level != "debug", // ParameterizedQueries=true => Don't show data in logs
		},
	)

	gormConfig := gorm.Config{
		Logger: newLogger,
	}

	switch cfg.Database.Type {
	case "postgres":
		// Session timezone is pinned to UTC: the application stores and
		// compares UTC instants everywhere, so a server-side timezone like
		// Asia/Shanghai would only skew date/time rendering and any
		// timezone-sensitive server-side expressions.
		dsn := fmt.Sprintf("host=%s port=%v user=%s password=%s dbname=%s sslmode=disable TimeZone=UTC", cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.Name)
		for i := 0; i <= 30; i++ {
			db, err = gorm.Open(postgres.Open(dsn), &gormConfig)
			if err == nil {
				break
			}
			logging.DefaultLogger().Warnf("failed to open database: %v", err)
			time.Sleep(500 * time.Millisecond)
		}

	default:
		db, err = gorm.Open(sqlite.Open(sqliteDSN(cfg.Database.Path)), &gormConfig)

	}

	if err != nil {
		return nil, err
	}

	applyPoolLimits(db, &cfg.Database)

	// Add OpenTelemetry tracing to GORM
	if err := db.Use(tracing.NewPlugin()); err != nil {
		logging.DefaultLogger().Warnf("failed to enable OpenTelemetry tracing for GORM: %v", err)
	}

	return db, nil
}

// Pool limits: without an explicit ceiling, database/sql opens unbounded
// connections under load — on PostgreSQL that can exhaust the server's
// max_connections and take the whole deployment down. The defaults are sized
// for Dydan's family-scale deployments; operators can override every value
// via config (database.max_open_conns etc.). Zero/negative values in config
// fall back to the defaults rather than silently disabling the cap.
const (
	defaultMaxOpenConns    = 25
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = time.Hour
	defaultConnMaxIdleTime = 10 * time.Minute
)

func applyPoolLimits(db *gorm.DB, cfg *config.DatabaseConfig) {
	sqlDB, err := db.DB()
	if err != nil {
		logging.DefaultLogger().Warnf("failed to configure connection pool: %v", err)
		return
	}
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = defaultMaxOpenConns
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = defaultMaxIdleConns
	}
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	lifetime := cfg.ConnMaxLifetime
	if lifetime <= 0 {
		lifetime = defaultConnMaxLifetime
	}
	sqlDB.SetConnMaxLifetime(lifetime)
	idleTime := cfg.ConnMaxIdleTime
	if idleTime <= 0 {
		idleTime = defaultConnMaxIdleTime
	}
	sqlDB.SetConnMaxIdleTime(idleTime)
}
