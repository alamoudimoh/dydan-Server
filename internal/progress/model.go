package progress

import "time"

type GoalMetric string

const (
	MetricTaskCompletions     GoalMetric = "TASK_COMPLETIONS"
	MetricCategoryCompletions GoalMetric = "CATEGORY_COMPLETIONS"
	MetricPointsEarned        GoalMetric = "POINTS_EARNED"
)

type GoalStatus string

const (
	GoalActive    GoalStatus = "ACTIVE"
	GoalCompleted GoalStatus = "COMPLETED"
	GoalArchived  GoalStatus = "ARCHIVED"
)

// Goal stores only the target definition. Progress is always derived from the
// authoritative completion/points ledgers.
type Goal struct {
	ID         int        `json:"id" gorm:"primaryKey"`
	CircleID   int        `json:"circleId" gorm:"not null;index:idx_goals_circle_status_dates,priority:1"`
	Name       string     `json:"name" gorm:"not null"`
	MetricType GoalMetric `json:"metricType" gorm:"not null"`
	Target     int        `json:"target" gorm:"not null"`
	CategoryID *int       `json:"categoryId,omitempty" gorm:"index"`
	StartDate  time.Time  `json:"startDate" gorm:"not null;index:idx_goals_circle_status_dates,priority:3"`
	EndDate    time.Time  `json:"endDate" gorm:"not null;index:idx_goals_circle_status_dates,priority:4"`
	Status     GoalStatus `json:"status" gorm:"not null;index:idx_goals_circle_status_dates,priority:2"`
	CreatedBy  int        `json:"createdBy" gorm:"not null"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}
