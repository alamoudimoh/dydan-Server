package chore

import (
	"context"
	"fmt"
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	lModel "github.com/alamoudimoh/dydan-server/internal/label/model"
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	pModel "github.com/alamoudimoh/dydan-server/internal/points"
	stModel "github.com/alamoudimoh/dydan-server/internal/subtask/model"
	syncModel "github.com/alamoudimoh/dydan-server/internal/sync/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// benchRepository seeds a representative family-scale dataset: one circle,
// `chores` active chores each with two assignees and a subtask.
func benchRepository(b *testing.B, chores int) (*ChoreRepository, int, int) {
	b.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		b.Fatal(err)
	}
	if err := db.AutoMigrate(
		&chModel.Chore{}, &chModel.ChoreHistory{}, &chModel.ChoreAssignees{},
		&chModel.TimeSession{}, &cModel.UserCircle{}, &pModel.PointsHistory{},
		&syncModel.SyncCursor{}, &lModel.Label{}, &chModel.ChoreLabels{},
		&stModel.SubTask{}, &uModel.User{}, &mModel.Meter{},
	); err != nil {
		b.Fatal(err)
	}
	repo := NewChoreRepository(db, &config.Config{Database: config.DatabaseConfig{Type: "sqlite"}})
	const circleID = 1
	for i := 0; i < chores; i++ {
		ch := chModel.Chore{
			Name:          fmt.Sprintf("chore-%d", i),
			CircleID:      circleID,
			CreatedBy:     1,
			UpdatedBy:     1,
			AssignedTo:    ptrInt(1),
			FrequencyType: chModel.FrequencyTypeOnce,
			Frequency:     1,
			IsActive:      true,
		}
		if err := db.Create(&ch).Error; err != nil {
			b.Fatal(err)
		}
		if err := db.Create(&chModel.ChoreAssignees{ChoreID: ch.ID, UserID: 1}).Error; err != nil {
			b.Fatal(err)
		}
		if err := db.Create(&chModel.ChoreAssignees{ChoreID: ch.ID, UserID: 2}).Error; err != nil {
			b.Fatal(err)
		}
	}
	return repo, circleID, 1
}

func ptrInt(i int) *int { return &i }

func BenchmarkGetChores100(b *testing.B) {
	benchmarkGetChores(b, 100)
}

func BenchmarkGetChores500(b *testing.B) {
	benchmarkGetChores(b, 500)
}

func benchmarkGetChores(b *testing.B, n int) {
	repo, circleID, userID := benchRepository(b, n)
	ctx := context.Background()
	// Warm up table probes once (HasTable checks inside GetChores).
	if _, err := repo.GetChores(ctx, circleID, userID, false, nil, false); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := repo.GetChores(ctx, circleID, userID, false, nil, false)
		if err != nil {
			b.Fatal(err)
		}
		if len(got) != n {
			b.Fatalf("got %d chores, want %d", len(got), n)
		}
	}
}
