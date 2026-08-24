package user

import (
	"context"
	"errors"
	"sync"
	"testing"

	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
)

// TestConcurrentDuplicateSignupUsername drives the real signup path with N
// concurrent requests for the same username. Exactly one may succeed, every
// loser must receive ErrUserAlreadyExists (never a raw driver error or other
// failure), and exactly one row may exist afterwards.
func TestConcurrentDuplicateSignupUsername(t *testing.T) {
	_, repo := newTokenHashHarness(t)

	const workers = 8
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := repo.CreateUser(context.Background(), &uModel.User{
				Username: "race-user",
				Email:    "race-user-" + string(rune('a'+n)) + "@example.com",
			})
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrUserAlreadyExists):
			// expected loser classification
		default:
			t.Fatalf("loser received an unexpected error (must be duplicate-classified): %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("exactly one signup must win, got %d", successes)
	}
	assertOneRaceRow(t, repo)
}

// TestConcurrentDuplicateSignupEmail covers the same race against the partial
// unique email index.
func TestConcurrentDuplicateSignupEmail(t *testing.T) {
	_, repo := newTokenHashHarness(t)

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := repo.CreateUser(context.Background(), &uModel.User{
				Username: "email-racer-" + string(rune('a'+n)),
				Email:    "shared-race@example.com",
			})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrUserAlreadyExists):
		default:
			t.Fatalf("unexpected error in email race: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("exactly one email signup must win, got %d", successes)
	}
}

// TestConstraintPathClassifiedAsDuplicate deterministically exercises the
// insert-time constraint violation (the branch the pre-check cannot close):
// two rows race past any pre-check by inserting directly through gorm, then
// the classifier must map the native driver error to ErrUserAlreadyExists.
func TestConstraintPathClassifiedAsDuplicate(t *testing.T) {
	db, repo := newTokenHashHarness(t)

	first := &uModel.User{Username: "constraint-dup", Email: "cd@example.com"}
	if _, err := repo.CreateUser(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	// Direct insert skips CreateUser's pre-check — this is exactly what the
	// losing request's INSERT produces after winning its own pre-check.
	rawErr := db.Create(&uModel.User{Username: "constraint-dup", Email: "other-cd@example.com"}).Error
	if rawErr == nil {
		t.Fatal("expected unique-constraint violation")
	}
	if !errors.Is(ErrUserAlreadyExists, ErrUserAlreadyExists) || !classifyDuplicateIdentity(rawErr) {
		t.Fatalf("constraint violation must classify as duplicate, got: %v", rawErr)
	}
	if classifyDuplicateIdentity(nil) {
		t.Fatal("sanity")
	}
}

// TestUnrelatedPersistenceFailureStaysUnclassified pins that arbitrary DB
// failures do not masquerade as duplicates.
func TestUnrelatedPersistenceFailureStaysUnclassified(t *testing.T) {
	db, repo := newTokenHashHarness(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	_ = sqlDB.Close()

	_, err = repo.CreateUser(context.Background(), &uModel.User{Username: "ghost", Email: "ghost@example.com"})
	if err == nil {
		t.Fatal("expected failure on closed database")
	}
	if errors.Is(err, ErrUserAlreadyExists) {
		t.Fatal("infrastructure failure must not be classified as a duplicate account")
	}
}

func assertOneRaceRow(t *testing.T, repo *UserRepository) {
	t.Helper()
	var count int64
	if err := repo.db.Model(&uModel.User{}).Where("username = ?", "race-user").Count(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("exactly one account row must exist after the race, got %d", count)
	}
}
