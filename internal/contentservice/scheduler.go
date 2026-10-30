package contentservice

import (
	"context"
	"log/slog"
	"time"
)

type Refresher interface {
	Refresh(context.Context, time.Time) error
}

func RunScheduler(ctx context.Context, interval time.Duration, refreshers ...Refresher) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	run := func(now time.Time) {
		for i, refresher := range refreshers {
			if ctx.Err() != nil {
				return
			}
			if err := refresher.Refresh(ctx, now); err != nil && ctx.Err() == nil {
				slog.Error("content refresh failed", "refresher_index", i, "error", err)
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	run(time.Now())
	if ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			run(now)
		}
	}
}
