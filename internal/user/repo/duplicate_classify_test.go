package user

import (
	"context"
	"errors"
	"testing"

	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"

	"github.com/glebarez/go-sqlite"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestClassifyDuplicateIdentityRealSQLiteViolations(t *testing.T) {
	db, repo := newTokenHashHarness(t)
	if _, err := repo.CreateUser(context.Background(), &uModel.User{Username: "dup-u", Email: "dup@example.com"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Bypass CreateUser's pre-check and insert directly to obtain the real
	// driver-level constraint error from the actual race path.
	rawUsernameDup := db.Create(&uModel.User{Username: "dup-u", Email: "other@example.com"}).Error
	var sqliteErr *sqlite.Error
	if !errors.As(rawUsernameDup, &sqliteErr) {
		t.Fatalf("expected native sqlite error, got %T: %v", rawUsernameDup, rawUsernameDup)
	}
	if !classifyDuplicateIdentity(rawUsernameDup) {
		t.Fatalf("username unique violation must classify as duplicate: %v", rawUsernameDup)
	}

	rawEmailDup := db.Create(&uModel.User{Username: "fresh-user", Email: "dup@example.com"}).Error
	if !classifyDuplicateIdentity(rawEmailDup) {
		t.Fatalf("email unique violation must classify as duplicate: %v", rawEmailDup)
	}
}

func TestClassifyDuplicateIdentityPostgresNativeErrors(t *testing.T) {
	cases := []struct {
		name string
		err  *pgconn.PgError
		want bool
	}{
		{
			name: "users username index",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "idx_users_username", TableName: "users"},
			want: true,
		},
		{
			name: "users email partial index",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "idx_email_not_null", TableName: "users"},
			want: true,
		},
		{
			name: "inline unique naming variant",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "uni_users_username"},
			want: true,
		},
		{
			name: "message-only marker with users table",
			err:  &pgconn.PgError{Code: "23505", TableName: "users", Message: `duplicate key value violates unique constraint`},
			want: false, // no identity marker present in the identifiers themselves
		},
		{
			name: "unrelated table constraint",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "meters_pkey", TableName: "meters", Message: "duplicate key value violates unique constraint \"meters_pkey\""},
			want: false,
		},
		{
			name: "foreign key violation is not a duplicate",
			err:  &pgconn.PgError{Code: "23503", ConstraintName: "users_circle_id_fkey", TableName: "users"},
			want: false,
		},
		{
			name: "check violation is not a duplicate",
			err:  &pgconn.PgError{Code: "23514", ConstraintName: "users_username_check"},
			want: false,
		},
	}
	for _, tc := range cases {
		if got := classifyDuplicateIdentity(tc.err); got != tc.want {
			t.Errorf("%s: classifyDuplicateIdentity = %v, want %v (err=%v)", tc.name, got, tc.want, tc.err)
		}
	}
}

func TestClassifyDuplicateIdentityOtherForms(t *testing.T) {
	if !classifyDuplicateIdentity(gorm.ErrDuplicatedKey) {
		t.Error("gorm.ErrDuplicatedKey must classify as duplicate")
	}
	if !classifyDuplicateIdentity(errors.Join(errors.New("wrap"), gorm.ErrDuplicatedKey)) {
		t.Error("wrapped gorm.ErrDuplicatedKey must classify as duplicate")
	}
	if classifyDuplicateIdentity(nil) {
		t.Error("nil error must not classify as duplicate")
	}
	if classifyDuplicateIdentity(errors.New("some unrelated database failure")) {
		t.Error("unrelated errors must not classify as duplicate")
	}
	if classifyDuplicateIdentity(gorm.ErrRecordNotFound) {
		t.Error("record-not-found must not classify as duplicate")
	}
}
