package chore

import (
	"context"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	chRepo "github.com/alamoudimoh/daydan-server/internal/chore/repo"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPersonalChoresAreAssignmentAndCircleScoped(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&chModel.Chore{}, &chModel.ChoreAssignees{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	userOne, userTwo := 1, 2
	chores := []chModel.Chore{
		{Name: "mine", CircleID: 10, AssignedTo: &userOne, IsActive: true, NextDueDate: &now},
		{Name: "theirs", CircleID: 10, AssignedTo: &userTwo, IsActive: true, NextDueDate: &now},
		{Name: "other circle", CircleID: 20, AssignedTo: &userOne, IsActive: true, NextDueDate: &now},
		{Name: "unassigned", CircleID: 10, IsActive: true, NextDueDate: &now},
	}
	if err = db.Create(&chores).Error; err != nil {
		t.Fatal(err)
	}
	shared := chModel.Chore{Name: "multi-assignee", CircleID: 10, IsActive: true, NextDueDate: &now}
	if err = db.Create(&shared).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&chModel.ChoreAssignees{ChoreID: shared.ID, UserID: userOne}).Error; err != nil {
		t.Fatal(err)
	}
	repo := chRepo.NewChoreRepository(db, &config.Config{Database: config.DatabaseConfig{Type: "sqlite"}})
	got, err := repo.GetPersonalChores(context.Background(), 10, userOne)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "mine" || got[1].Name != "multi-assignee" {
		t.Fatalf("unexpected personal chores: %#v", got)
	}
}
