package model

import "time"

type Unit string

const (
	UnitKilometers Unit = "km"
	UnitMiles      Unit = "miles"
	UnitHours      Unit = "hours"
	UnitCycles     Unit = "cycles"
	UnitCount      Unit = "count"
)

func (u Unit) Valid() bool {
	switch u {
	case UnitKilometers, UnitMiles, UnitHours, UnitCycles, UnitCount:
		return true
	default:
		return false
	}
}

type Meter struct {
	ID             int             `json:"id" gorm:"primaryKey"`
	CircleID       int             `json:"circleId" gorm:"column:circle_id;not null;index"`
	CreatedBy      int             `json:"createdBy" gorm:"column:created_by;not null"`
	ThingID        *int            `json:"assetId,omitempty" gorm:"column:thing_id;index"`
	Name           string          `json:"name" gorm:"column:name;not null"`
	Unit           Unit            `json:"unit" gorm:"column:unit;not null"`
	CurrentReading int64           `json:"currentReading" gorm:"column:current_reading;not null"`
	CreatedAt      time.Time       `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt      time.Time       `json:"updatedAt" gorm:"column:updated_at"`
	UpdateReminder *UpdateReminder `json:"updateReminder,omitempty" gorm:"foreignKey:MeterID;references:ID"`
}

// UpdateReminder links a meter to the one ordinary recurring chore that asks
// for a fresh manual reading. The chore remains the scheduling authority.
type UpdateReminder struct {
	MeterID           int        `json:"meterId" gorm:"column:meter_id;primaryKey"`
	ChoreID           int        `json:"choreId" gorm:"column:chore_id;not null;uniqueIndex"`
	LastCheckedAt     *time.Time `json:"lastCheckedAt,omitempty" gorm:"column:last_checked_at"`
	NextUpdateDue     *time.Time `json:"nextUpdateDue,omitempty" gorm:"-"`
	UpdateStatus      string     `json:"updateStatus" gorm:"-"`
	FrequencyType     string     `json:"frequencyType" gorm:"-"`
	Frequency         int        `json:"frequency" gorm:"-"`
	FrequencyMetadata any        `json:"frequencyMetadata,omitempty" gorm:"-"`
	AssignedTo        *int       `json:"assignedTo,omitempty" gorm:"-"`
	CurrentReading    int64      `json:"currentReading" gorm:"-"`
	Unit              Unit       `json:"unit" gorm:"-"`
	MeterName         string     `json:"meterName" gorm:"-"`
}

func (UpdateReminder) TableName() string { return "meter_update_reminders" }

type ReadingKind string

const (
	ReadingKindReading    ReadingKind = "reading"
	ReadingKindCorrection ReadingKind = "correction"
)

type Reading struct {
	ID              int         `json:"id" gorm:"primaryKey"`
	MeterID         int         `json:"meterId" gorm:"column:meter_id;not null;index"`
	Reading         int64       `json:"reading" gorm:"column:reading;not null"`
	PreviousReading *int64      `json:"previousReading,omitempty" gorm:"column:previous_reading"`
	Kind            ReadingKind `json:"kind" gorm:"column:kind;not null"`
	Note            *string     `json:"note,omitempty" gorm:"column:note"`
	RecordedBy      int         `json:"recordedBy" gorm:"column:recorded_by;not null"`
	RecordedAt      time.Time   `json:"recordedAt" gorm:"column:recorded_at;not null;index"`
}

func (Reading) TableName() string { return "meter_readings" }

// ChoreSchedule composes a meter interval with a chore's existing calendar schedule.
// Baseline is advanced only on completion, so the target is never an arbitrary absolute trigger.
type ChoreSchedule struct {
	ChoreID            int        `json:"choreId" gorm:"column:chore_id;primaryKey"`
	MeterID            int        `json:"meterId" gorm:"column:meter_id;not null;index"`
	Interval           int64      `json:"interval" gorm:"column:interval;not null"`
	WarnBefore         int64      `json:"warnBefore" gorm:"column:warn_before;not null;default:0"`
	Baseline           int64      `json:"baseline" gorm:"column:baseline;not null"`
	CalendarDueDate    *time.Time `json:"calendarDueDate,omitempty" gorm:"column:calendar_due_date"`
	CurrentReading     int64      `json:"currentReading" gorm:"-"`
	Target             int64      `json:"target" gorm:"-"`
	Used               int64      `json:"used" gorm:"-"`
	Remaining          int64      `json:"remaining" gorm:"-"`
	Progress           int        `json:"progress" gorm:"-"`
	DueByMeter         bool       `json:"dueByMeter" gorm:"-"`
	DueByTime          bool       `json:"dueByTime" gorm:"-"`
	DueReason          string     `json:"dueReason,omitempty" gorm:"-"`
	Status             string     `json:"status" gorm:"-"`
	ApproachingByMeter bool       `json:"approachingByMeter" gorm:"-"`
	Unit               Unit       `json:"unit" gorm:"-"`
	MeterName          string     `json:"meterName" gorm:"-"`
}

func (ChoreSchedule) TableName() string { return "chore_meter_schedules" }

type ChoreScheduleRequest struct {
	MeterID    int   `json:"meterId" binding:"required"`
	Interval   int64 `json:"interval" binding:"required,gt=0"`
	WarnBefore int64 `json:"warnBefore" binding:"omitempty,gte=0"`
}
