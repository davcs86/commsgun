// Package constants holds every limit, duration and cap; no other package may hard-code one.
package constants

import "time"

const (
	MaxBodyBytes         = 262144
	MaxRecipients        = 10
	MaxAttempts          = 8
	BackoffBase          = 30 * time.Second
	BackoffMax           = time.Hour
	BackoffJitter        = 0.2
	MaxSendWindow        = 24 * time.Hour
	Retention            = 30 * 24 * time.Hour
	EnqueueMaxConcurrent = 32
	MaxQueueDepth        = 10000
	LeaseDuration        = 2 * time.Minute
	SendTimeout          = 15 * time.Second
	WorkerTick           = time.Second
	DispatchBatch        = 4
	PollBatch            = 2
	WorkerFresh          = 30 * time.Second
	BacklogThreshold     = 15 * time.Minute
	SweepInterval        = time.Hour
	MonthlyQuotaPause    = 6 * time.Hour
	MinSecretLen         = 32
	IdempotencyKeyMaxLen = 200
	SubjectMaxLen        = 998
	SMSBodyMaxLen        = 1600
	AttemptMessageMaxLen = 300
	AdminPageSize        = 50
	SQLiteBusyTimeout    = 5 * time.Second
	RetryAfterBusy       = time.Second
	PingTimeout          = 2 * time.Second
	MinuteCounterMaxAge  = time.Hour
	DayCounterMaxAge     = 48 * time.Hour

	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 15 * time.Second
	WriteTimeout      = 30 * time.Second
	ShutdownTimeout   = 10 * time.Second
	HTTPClientTimeout = 20 * time.Second

	ProviderResponseMaxBytes = 64 << 10
	RateLimitDefaultPause    = time.Second
	RateLimitMaxPause        = time.Hour

	AdminDefaultRange = time.Hour
	AdminMaxRange     = 31 * 24 * time.Hour
	AdminFilterMaxLen = 200
	AdminFacetCap     = 200
)

// BucketStep maps a stats range up to MaxRange onto a series bucket; the last step has no bound.
type BucketStep struct{ MaxRange, Bucket time.Duration }

// BucketSteps is ordered by MaxRange; ranges beyond the last bound use BucketLast.
var BucketSteps = []BucketStep{
	{time.Hour, time.Minute},
	{6 * time.Hour, 5 * time.Minute},
	{24 * time.Hour, 15 * time.Minute},
	{7 * 24 * time.Hour, time.Hour},
	{14 * 24 * time.Hour, 6 * time.Hour},
}

const BucketLast = 24 * time.Hour

// PollSchedule lists bounce-poll offsets from sent_at; callers must not mutate it.
var PollSchedule = []time.Duration{
	time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour,
	12 * time.Hour, 24 * time.Hour, 48 * time.Hour,
}
