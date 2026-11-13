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
)

// PollSchedule lists bounce-poll offsets from sent_at; callers must not mutate it.
var PollSchedule = []time.Duration{
	time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour,
	12 * time.Hour, 24 * time.Hour, 48 * time.Hour,
}
