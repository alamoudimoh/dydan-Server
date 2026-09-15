package chore

import (
	"context"
	"fmt"
	"sort"
	"time"

	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	lModel "github.com/alamoudimoh/dydan-server/internal/label/model"
)

// CalendarOccurrence is a read-only view of authoritative scheduled work.
// Projected occurrences are never persisted and only the current occurrence is
// actionable through the normal chore completion endpoint.
type CalendarOccurrence struct {
	OccurrenceID        string                `json:"occurrenceId"`
	ChoreID             int                   `json:"choreId"`
	Name                string                `json:"name"`
	DueAt               time.Time             `json:"dueAt"`
	Actionable          bool                  `json:"actionable"`
	Projected           bool                  `json:"projected"`
	Overdue             bool                  `json:"overdue"`
	FrequencyType       chModel.FrequencyType `json:"frequencyType"`
	Points              *int                  `json:"points,omitempty"`
	RequireApproval     bool                  `json:"requireApproval"`
	Status              chModel.Status        `json:"status"`
	MeterSchedule       interface{}           `json:"meterSchedule,omitempty"`
	MeterUpdateReminder interface{}           `json:"meterUpdateReminder,omitempty"`
	Category            *lModel.Label         `json:"category,omitempty"`
}

type CalendarProjection struct {
	Occurrences []CalendarOccurrence `json:"occurrences"`
	Overdue     []CalendarOccurrence `json:"overdue"`
	TimeZone    string               `json:"timeZone"`
}

func occurrenceID(choreID int, due time.Time) string {
	return fmt.Sprintf("%d:%s", choreID, due.UTC().Format(time.RFC3339Nano))
}

func deterministicCalendarRecurrence(chore *chModel.Chore) bool {
	if chore.IsRolling {
		return false
	}
	switch chore.FrequencyType {
	case chModel.FrequencyTypeDaily, chModel.FrequencyTypeWeekly,
		chModel.FrequencyTypeMonthly, chModel.FrequencyTypeYearly,
		chModel.FrequencyTypeDayOfTheWeek, chModel.FrequencyTypeDayOfTheMonth:
		return true
	case chModel.FrequencyTypeInterval:
		return chore.FrequencyMetadataV2 != nil && chore.FrequencyMetadataV2.Unit != nil
	default:
		return false
	}
}

// GenerateCalendarProjection advances an in-memory chore copy using the same
// recurrence function used after completion. It has no database or event side
// effects and deliberately projects only deterministic schedules.
func GenerateCalendarProjection(ctx context.Context, chores []*chModel.Chore, from, to, now time.Time, timezone string) (CalendarProjection, error) {
	result := CalendarProjection{Occurrences: []CalendarOccurrence{}, Overdue: []CalendarOccurrence{}, TimeZone: timezone}
	for _, source := range chores {
		if source == nil || !source.IsActive || source.NextDueDate == nil {
			continue
		}
		copyChore := *source
		due := source.NextDueDate.UTC()
		for sequence := 0; ; sequence++ {
			if !due.Before(from) && due.Before(to) {
				result.Occurrences = append(result.Occurrences, calendarOccurrence(source, due, sequence > 0, sequence == 0, now))
			} else if sequence == 0 && due.Before(from) && due.Before(now) {
				result.Overdue = append(result.Overdue, calendarOccurrence(source, due, false, true, now))
			}
			if !deterministicCalendarRecurrence(&copyChore) || !due.Before(to) {
				break
			}
			copyChore.NextDueDate = &due
			next, err := scheduleNextDueDate(ctx, &copyChore, due)
			if err != nil {
				return CalendarProjection{}, fmt.Errorf("project chore %d: %w", source.ID, err)
			}
			if next == nil || !next.After(due) {
				break
			}
			due = next.UTC()
		}
	}
	sort.Slice(result.Occurrences, func(i, j int) bool { return result.Occurrences[i].DueAt.Before(result.Occurrences[j].DueAt) })
	sort.Slice(result.Overdue, func(i, j int) bool { return result.Overdue[i].DueAt.Before(result.Overdue[j].DueAt) })
	return result, nil
}

func calendarOccurrence(chore *chModel.Chore, due time.Time, projected, actionable bool, now time.Time) CalendarOccurrence {
	return CalendarOccurrence{
		OccurrenceID: occurrenceID(chore.ID, due), ChoreID: chore.ID, Name: chore.Name,
		DueAt: due, Actionable: actionable, Projected: projected, Overdue: actionable && due.Before(now),
		FrequencyType: chore.FrequencyType, Points: chore.Points, RequireApproval: chore.RequireApproval,
		Status: chore.Status, MeterSchedule: chore.MeterSchedule, MeterUpdateReminder: chore.MeterUpdateReminder,
		Category: chore.Category,
	}
}
