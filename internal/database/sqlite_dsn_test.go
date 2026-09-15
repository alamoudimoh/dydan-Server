package database

import "testing"

// Regression: the SQLite DSN must enable WAL, a busy_timeout, and NORMAL
// synchronous mode so concurrent HTTP handlers and background schedulers do
// not fail with "database is locked" and crashes cannot tear the DB.
func TestSQLiteDSNAppendsDurabilityPragmas(t *testing.T) {
	got := sqliteDSN("data/dydan.db")
	want := "data/dydan.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	if got != want {
		t.Fatalf("sqliteDSN() = %q, want %q", got, want)
	}
}

func TestSQLiteDSNKeepsCallerProvidedDSN(t *testing.T) {
	in := "file:test.db?mode=memory&cache=shared"
	if got := sqliteDSN(in); got != in {
		t.Fatalf("sqliteDSN(%q) = %q, want unchanged", in, got)
	}
}
