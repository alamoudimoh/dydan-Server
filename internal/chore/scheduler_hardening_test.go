package chore

import (
	"context"
	"testing"
	"time"

	chModel "donetick.com/core/internal/chore/model"
)

func metadataAt(t *testing.T, zone, local string) *chModel.FrequencyMetadata {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	clock, err := time.ParseInLocation("2006-01-02 15:04", local, loc)
	if err != nil {
		t.Fatal(err)
	}
	return &chModel.FrequencyMetadata{Time: clock.Format(time.RFC3339), Timezone: zone}
}

func requireLocal(t *testing.T, got *time.Time, zone, want string) {
	t.Helper()
	if got == nil {
		t.Fatal("got nil due date")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	if value := got.In(loc).Format("2006-01-02 15:04 MST"); value != want {
		t.Fatalf("got %s (%s UTC), want %s", value, got.UTC().Format(time.RFC3339), want)
	}
}

func TestCalendarRecurrencesPreserveWallClockAcrossTimezones(t *testing.T) {
	tests := []struct {
		name, zone, due, completed, want string
		typeName                         chModel.FrequencyType
	}{
		{"Riyadh daily", "Asia/Riyadh", "2026-08-11 20:00", "2026-08-11 21:00", "2026-08-12 20:00 +03", chModel.FrequencyTypeDaily},
		{"New York spring daily", "America/New_York", "2026-03-07 08:00", "2026-03-07 09:00", "2026-03-08 08:00 EDT", chModel.FrequencyTypeDaily},
		{"New York fall daily", "America/New_York", "2026-10-31 08:00", "2026-10-31 09:00", "2026-11-01 08:00 EST", chModel.FrequencyTypeDaily},
		{"New York weekly", "America/New_York", "2026-03-04 18:30", "2026-03-04 19:00", "2026-03-11 18:30 EDT", chModel.FrequencyTypeWeekly},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loc, _ := time.LoadLocation(tc.zone)
			due, _ := time.ParseInLocation("2006-01-02 15:04", tc.due, loc)
			completed, _ := time.ParseInLocation("2006-01-02 15:04", tc.completed, loc)
			chore := &chModel.Chore{FrequencyType: tc.typeName, NextDueDate: &due, FrequencyMetadataV2: metadataAt(t, tc.zone, tc.due)}
			got, err := scheduleNextDueDate(context.Background(), chore, completed)
			if err != nil {
				t.Fatal(err)
			}
			requireLocal(t, got, tc.zone, tc.want)
		})
	}
}

func TestDaysOfWeekProgressionAndBoundaries(t *testing.T) {
	zone := "Asia/Riyadh"
	loc, _ := time.LoadLocation(zone)
	days := []*string{jsonPtr("monday"), jsonPtr("wednesday"), jsonPtr("saturday")}
	meta := metadataAt(t, zone, "2026-08-10 19:00")
	meta.Days = days
	due, _ := time.ParseInLocation("2006-01-02 15:04", "2026-08-10 19:00", loc)
	chore := &chModel.Chore{FrequencyType: chModel.FrequencyTypeDayOfTheWeek, NextDueDate: &due, FrequencyMetadataV2: meta}
	for _, want := range []string{"2026-08-12 19:00 +03", "2026-08-15 19:00 +03", "2026-08-17 19:00 +03"} {
		next, err := scheduleNextDueDate(context.Background(), chore, due.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		requireLocal(t, next, zone, want)
		due = *next
		chore.NextDueDate = next
	}

	// Initial scheduling includes a selected day at its configured wall time.
	start, _ := time.ParseInLocation("2006-01-02 15:04", "2026-08-10 08:00", loc)
	initial, err := InitialDueDate(context.Background(), &chModel.Chore{FrequencyType: chModel.FrequencyTypeDayOfTheWeek, FrequencyMetadataV2: meta}, start)
	if err != nil {
		t.Fatal(err)
	}
	requireLocal(t, initial, zone, "2026-08-10 19:00 +03")
}

func TestMonthlyAndYearlyClampInvalidCalendarDays(t *testing.T) {
	tests := []struct {
		name, due, want string
		frequencyType   chModel.FrequencyType
	}{
		{"January 31", "2025-01-31 20:00", "2025-02-28 20:00 UTC", chModel.FrequencyTypeMonthly},
		{"leap January 31", "2024-01-31 20:00", "2024-02-29 20:00 UTC", chModel.FrequencyTypeMonthly},
		{"March 31", "2025-03-31 20:00", "2025-04-30 20:00 UTC", chModel.FrequencyTypeMonthly},
		{"leap birthday", "2024-02-29 08:00", "2025-02-28 08:00 UTC", chModel.FrequencyTypeYearly},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			due, _ := time.ParseInLocation("2006-01-02 15:04", tc.due, time.UTC)
			chore := &chModel.Chore{FrequencyType: tc.frequencyType, NextDueDate: &due, FrequencyMetadataV2: metadataAt(t, "UTC", tc.due)}
			got, err := scheduleNextDueDate(context.Background(), chore, due)
			if err != nil {
				t.Fatal(err)
			}
			requireLocal(t, got, "UTC", tc.want)
		})
	}
}

func TestIntervalAnchorsToScheduledDueUnlessRolling(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	due := time.Date(2026, 8, 11, 20, 0, 0, 0, loc)
	unit := "days"
	meta := &chModel.FrequencyMetadata{Time: due.Format(time.RFC3339), Timezone: "UTC", Unit: &unit}
	for _, tc := range []struct {
		name      string
		completed time.Time
		rolling   bool
		want      string
	}{
		{"early", due.Add(-24 * time.Hour), false, "2026-08-21 20:00 UTC"},
		{"exact", due, false, "2026-08-21 20:00 UTC"},
		{"late", due.Add(3 * 24 * time.Hour), false, "2026-08-21 20:00 UTC"},
		{"rolling late", due.Add(3 * 24 * time.Hour), true, "2026-08-24 20:00 UTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chore := &chModel.Chore{FrequencyType: chModel.FrequencyTypeInterval, Frequency: 10, NextDueDate: &due, FrequencyMetadataV2: meta, IsRolling: tc.rolling}
			got, err := scheduleNextDueDate(context.Background(), chore, tc.completed)
			if err != nil {
				t.Fatal(err)
			}
			requireLocal(t, got, "UTC", tc.want)
		})
	}
}

func TestNonRepeatingAndInvalidMetadata(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	for _, frequencyType := range []chModel.FrequencyType{chModel.FrequencyTypeOnce, chModel.FrequencyTypeNoRepeat, chModel.FrequencyTypeTrigger} {
		got, err := scheduleNextDueDate(context.Background(), &chModel.Chore{FrequencyType: frequencyType}, now)
		if err != nil || got != nil {
			t.Fatalf("%s generated another occurrence: %v, %v", frequencyType, got, err)
		}
	}
	for name, chore := range map[string]*chModel.Chore{
		"time": {FrequencyType: chModel.FrequencyTypeInterval, Frequency: 1, FrequencyMetadataV2: &chModel.FrequencyMetadata{Timezone: "UTC", Time: "08/11/2026", Unit: jsonPtr("days")}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := scheduleNextDueDate(context.Background(), chore, now); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestScheduleDefinitionChangeDetectionIgnoresUnrelatedEdits(t *testing.T) {
	frequency := 1
	meta := metadataAt(t, "Asia/Riyadh", "2026-08-11 20:00")
	old := &chModel.Chore{FrequencyType: chModel.FrequencyTypeDaily, Frequency: frequency, FrequencyMetadataV2: meta}
	req := &ChoreReq{FrequencyType: old.FrequencyType, Frequency: &frequency, FrequencyMetadata: meta}
	if scheduleDefinitionsDiffer(old, req) {
		t.Fatal("identical schedule was treated as changed")
	}
	// Name, points, description, assignment and approval are intentionally absent
	// from scheduleDefinitionsDiffer and therefore cannot reset the due schedule.
	changedFrequency := 2
	req.Frequency = &changedFrequency
	if !scheduleDefinitionsDiffer(old, req) {
		t.Fatal("recurrence edit was not detected")
	}
}
