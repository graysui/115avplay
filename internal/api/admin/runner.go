package admin

import "context"

// TaskRunner is implemented by the application runtime and lets the admin HTTP
// handlers trigger real background work instead of only enqueuing job rows.
type TaskRunner interface {
	TriggerRankings(ctx context.Context, period, rankingType, year string, limit int) (string, error)
	TriggerScrape(ctx context.Context, dateField, startDate, endDate string, includeFailed, includeExempt bool, limit int) (string, int, error)
	TriggerSync30D(ctx context.Context) (string, error)
	TriggerImportFull(ctx context.Context) (string, error)
	TriggerScan(ctx context.Context, rootCID, mode string) (string, error)
}
