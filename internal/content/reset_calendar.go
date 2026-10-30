package content

import "context"

// ResetCalendarSnapshot contains the public AIHOT reset records. Dates are
// source values: OccurredOn is verified independently of ConfirmedAt (the
// confirmation post date), and Schedule remains an estimate when present.
type ResetCalendarSnapshot struct {
	Timezone    string               `json:"timezone"`
	Today       *string              `json:"today"`
	CheckedAt   *string              `json:"checkedAt"`
	HistoryFrom string               `json:"historyFrom"`
	Events      []ResetCalendarEvent `json:"events"`
	Meta        ResetCalendarMeta    `json:"meta"`
}

type ResetCalendarMeta struct {
	Stale     bool   `json:"stale"`
	LastError string `json:"lastError"`
}

type ResetCalendarEvent struct {
	ID           string                 `json:"id"`
	Type         string                 `json:"type"`
	DisplayLabel string                 `json:"displayLabel"`
	Status       string                 `json:"status"`
	Title        string                 `json:"title"`
	Scope        string                 `json:"scope"`
	OccurredOn   *string                `json:"occurredOn"`
	ConfirmedAt  *string                `json:"confirmedAt"`
	CreatedAt    string                 `json:"createdAt"`
	UpdatedAt    string                 `json:"updatedAt"`
	Schedule     *ResetCalendarSchedule `json:"schedule"`
	Posts        []ResetCalendarPost    `json:"posts"`
}

type ResetCalendarSchedule struct {
	Precision string  `json:"precision"`
	From      *string `json:"from"`
	Through   *string `json:"through"`
	Label     string  `json:"label"`
}

type ResetCalendarPost struct {
	ID          string `json:"id"`
	PublishedAt string `json:"publishedAt"`
	Stage       string `json:"stage"`
	Text        string `json:"text"`
	URL         string `json:"url"`
}

// ResetCalendarStore is an optional narrow extension implemented by stores
// that persist the public calendar snapshot.
type ResetCalendarStore interface {
	SaveResetCalendar(context.Context, ResetCalendarSnapshot) error
	LoadResetCalendar(context.Context) (ResetCalendarSnapshot, error)
}
