package chore

import (
	"context"
	"testing"
	"time"

	chModel "donetick.com/core/internal/chore/model"
)

func pointer[T any](value T) *T { return &value }

func TestCalendarProjectionDeterministicAndSideEffectFree(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Riyadh")
	due := time.Date(2026, 8, 11, 18, 0, 0, 0, location).UTC()
	points := 5
	chore := &chModel.Chore{ID: 7, Name: "Weekly review", IsActive: true, NextDueDate: &due, FrequencyType: chModel.FrequencyTypeWeekly, Points: &points}
	original := *chore.NextDueDate
	projection, err := GenerateCalendarProjection(context.Background(), []*chModel.Chore{chore}, due.Add(-time.Hour), due.AddDate(0, 0, 22), due.Add(-time.Hour), "Asia/Riyadh")
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Occurrences) != 4 {
		t.Fatalf("got %d occurrences", len(projection.Occurrences))
	}
	for index, occurrence := range projection.Occurrences {
		if occurrence.DueAt.In(location).Hour() != 18 {
			t.Fatalf("occurrence %d shifted wall clock", index)
		}
		if occurrence.Actionable != (index == 0) || occurrence.Projected != (index > 0) {
			t.Fatalf("unsafe state: %+v", occurrence)
		}
	}
	if !chore.NextDueDate.Equal(original) {
		t.Fatal("projection mutated next due date")
	}
	if projection.Occurrences[0].OccurrenceID == projection.Occurrences[1].OccurrenceID {
		t.Fatal("recurrences lack stable unique identity")
	}
}

func TestCalendarProjectionDaysOfWeekAndDST(t *testing.T) {
	location, _ := time.LoadLocation("America/New_York")
	due := time.Date(2026, 3, 6, 8, 0, 0, 0, location).UTC()
	days := []*string{pointer("monday"), pointer("wednesday"), pointer("saturday")}
	clock := time.Date(2026, 3, 6, 8, 0, 0, 0, location).Format(time.RFC3339)
	chore := &chModel.Chore{ID: 2, Name: "Pattern", IsActive: true, NextDueDate: &due, FrequencyType: chModel.FrequencyTypeDayOfTheWeek, FrequencyMetadataV2: &chModel.FrequencyMetadata{Days: days, Time: clock, Timezone: location.String()}}
	projection, err := GenerateCalendarProjection(context.Background(), []*chModel.Chore{chore}, due, due.AddDate(0, 0, 12), due, location.String())
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Weekday{time.Friday, time.Saturday, time.Monday, time.Wednesday, time.Saturday, time.Monday, time.Wednesday}
	if len(projection.Occurrences) != len(want) {
		t.Fatalf("got %d occurrences", len(projection.Occurrences))
	}
	for i, occurrence := range projection.Occurrences {
		local := occurrence.DueAt.In(location)
		if local.Weekday() != want[i] || local.Hour() != 8 {
			t.Fatalf("%d: got %v", i, local)
		}
	}
}

func TestCalendarProjectionTruthfulUnknownAndOnceSchedules(t *testing.T) {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	types := []chModel.FrequencyType{chModel.FrequencyTypeOnce, chModel.FrequencyTypeNoRepeat, chModel.FrequencyTypeTrigger, chModel.FrequencyTypeAdaptive}
	for index, frequency := range types {
		due := from.AddDate(0, 0, index+1)
		chore := &chModel.Chore{ID: index + 1, Name: string(frequency), IsActive: true, NextDueDate: &due, FrequencyType: frequency}
		projection, err := GenerateCalendarProjection(context.Background(), []*chModel.Chore{chore}, from, from.AddDate(0, 0, 30), from, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		if len(projection.Occurrences) != 1 {
			t.Fatalf("%s invented future dates: %+v", frequency, projection.Occurrences)
		}
	}
	rollingDue := from.AddDate(0, 0, 1)
	unit := "days"
	rolling := &chModel.Chore{ID: 9, Name: "completion anchored", IsActive: true, IsRolling: true, NextDueDate: &rollingDue, FrequencyType: chModel.FrequencyTypeInterval, Frequency: 10, FrequencyMetadataV2: &chModel.FrequencyMetadata{Unit: &unit}}
	projection, _ := GenerateCalendarProjection(context.Background(), []*chModel.Chore{rolling}, from, from.AddDate(0, 0, 30), from, "UTC")
	if len(projection.Occurrences) != 1 {
		t.Fatal("rolling interval projected fictional dates")
	}
}

func TestCalendarProjectionMonthlyClampingAndLeapYear(t *testing.T) {
	for _, day := range []int{28, 29, 30, 31} {
		due := time.Date(2024, 1, day, 8, 0, 0, 0, time.UTC)
		chore := &chModel.Chore{ID: day, Name: "monthly", IsActive: true, NextDueDate: &due, FrequencyType: chModel.FrequencyTypeMonthly}
		projection, err := GenerateCalendarProjection(context.Background(), []*chModel.Chore{chore}, due, time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC), due, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		if len(projection.Occurrences) != 3 {
			t.Fatalf("day %d got %d", day, len(projection.Occurrences))
		}
		if day > 29 && projection.Occurrences[1].DueAt.Day() != 29 {
			t.Fatalf("day %d did not use authoritative leap-February clamp", day)
		}
	}
	leap := time.Date(2024, 2, 29, 8, 0, 0, 0, time.UTC)
	yearly := &chModel.Chore{ID: 50, Name: "yearly", IsActive: true, NextDueDate: &leap, FrequencyType: chModel.FrequencyTypeYearly}
	projection, _ := GenerateCalendarProjection(context.Background(), []*chModel.Chore{yearly}, leap, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), leap, "UTC")
	if projection.Occurrences[1].DueAt.Day() != 28 {
		t.Fatal("yearly leap semantics changed")
	}
}

func TestCalendarProjectionSeparatesOverdueAndBoundsRange(t *testing.T) {
	from := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	overdue := from.Add(-24 * time.Hour)
	before := &chModel.Chore{ID: 1, Name: "late", IsActive: true, NextDueDate: &overdue, FrequencyType: chModel.FrequencyTypeOnce}
	after := from.AddDate(0, 0, 8)
	beyond := &chModel.Chore{ID: 2, Name: "later", IsActive: true, NextDueDate: &after, FrequencyType: chModel.FrequencyTypeOnce}
	projection, _ := GenerateCalendarProjection(context.Background(), []*chModel.Chore{before, beyond}, from, from.AddDate(0, 0, 7), from, "UTC")
	if len(projection.Overdue) != 1 || len(projection.Occurrences) != 0 {
		t.Fatalf("bad range projection: %+v", projection)
	}
}
