package user

import (
	"errors"
	"strings"

	"github.com/glebarez/go-sqlite"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// classifyDuplicateIdentity reports whether err is a database unique-constraint
// violation on the users identity columns (username / email). Only those
// constraints are mapped to ErrUserAlreadyExists by CreateUser; every other
// constraint violation or database error propagates unchanged.
//
// Classification uses native driver error types for both supported dialects:
//
//   - PostgreSQL (pgx): *pgconn.PgError with SQLSTATE 23505 (unique_violation),
//     narrowed to the users identity columns via constraint/table/message
//     markers.
//   - SQLite (glebarez/go-sqlite): *sqlite.Error with result code
//     SQLITE_CONSTRAINT (19) / SQLITE_CONSTRAINT_UNIQUE (2067), narrowed the
//     same way. SQLite reports the offending columns/constraint only in the
//     message text, so a narrowly scoped, tested marker match is unavoidable
//     there.
//
// gorm.ErrDuplicatedKey is accepted as a third form so classification keeps
// working if GORM error translation is enabled in a future configuration.
func classifyDuplicateIdentity(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}

	var pgErr *pgconn.PgError
	if ok := errors.As(err, &pgErr); ok && pgErr.Code == "23505" {
		return usersIdentityMarker(pgErr.ConstraintName) ||
			usersIdentityMarker(pgErr.ColumnName) ||
			(pgErr.TableName == "users" && usersIdentityMarker(pgErr.Message))
	}

	var sqliteErr *sqlite.Error
	if ok := errors.As(err, &sqliteErr); ok && isSQLiteUniqueViolation(sqliteErr.Code()) {
		return usersIdentityMarker(sqliteErr.Error())
	}

	return false
}

// isSQLiteUniqueViolation matches both the base constraint code (19) and its
// unique-specific extended code (2067).
func isSQLiteUniqueViolation(code int) bool {
	return code == 19 || code == 2067 // SQLITE_CONSTRAINT / SQLITE_CONSTRAINT_UNIQUE
}

// usersIdentityMarker checks whether a driver-supplied identifier references
// the users identity columns. Constraint/index naming varies across GORM
// versions ("users_username", "idx_users_username", "uni_users_username",
// "idx_email_not_null", ...), so the check looks for the column stem inside
// the identifier.
func usersIdentityMarker(identifier string) bool {
	n := strings.ToLower(identifier)
	if n == "" {
		return false
	}
	// Require a table qualifier or known prefix shape so an unrelated index
	// that happens to contain "email" cannot be misclassified: accepted forms
	// contain "users" + the column, or are the partial-email index name.
	switch {
	case containsAll(n, "users", "username"):
		return true
	case containsAll(n, "users", "email"):
		return true
	case strings.Contains(n, "idx_email"):
		// Partial unique index on users.email created by the model tag.
		return true
	default:
		return false
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
