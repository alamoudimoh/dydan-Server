package preferences

// NotificationDefaults is the single canonical Settings default contract.
type NotificationDefaults struct {
	TaskReminders, RoutineNudges, FamilyActivity, Rewards bool
	WeeklySummary, QuietHours, PushChannel, EmailChannel  bool
}

func DefaultNotificationSettings() NotificationDefaults {
	return NotificationDefaults{TaskReminders: true, RoutineNudges: true, Rewards: true, PushChannel: true}
}
