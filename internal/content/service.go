package content

import "context"

type Reader interface {
	Feed(context.Context, Query) (Page, error)
	Entry(context.Context, string) (EntryResult, error)
	Status(context.Context) (Meta, error)
}

type ResetCalendarReader interface {
	ResetCalendar(context.Context) (ResetCalendarSnapshot, error)
}
