// Package model holds the types shared by the store, handlers and worker.
package model

import "time"

type Channel string

const (
	ChannelEmail Channel = "email"
	ChannelSMS   Channel = "sms"
)

type State string

const (
	StateQueued     State = "queued"
	StateSending    State = "sending"
	StateSent       State = "sent"
	StateDelivered  State = "delivered"
	StateBounced    State = "bounced"
	StateComplained State = "complained"
	StateFailed     State = "failed"
	StateExpired    State = "expired"
)

// Final reports whether the message can no longer change state.
func (s State) Final() bool {
	switch s {
	case StateDelivered, StateBounced, StateComplained, StateFailed, StateExpired:
		return true
	}
	return false
}

type ErrorType string

const (
	ErrTimeout             ErrorType = "timeout"
	ErrNetwork             ErrorType = "network"
	ErrProviderUnavailable ErrorType = "provider_unavailable"
	ErrProviderRejected    ErrorType = "provider_rejected"
	ErrInvalidRecipient    ErrorType = "invalid_recipient"
	ErrSenderNotVerified   ErrorType = "sender_not_verified"
	ErrAuth                ErrorType = "auth"
	ErrBounced             ErrorType = "bounced"
	ErrComplained          ErrorType = "complained"
	ErrExpired             ErrorType = "expired"
	ErrUnknown             ErrorType = "unknown"
)

type Outcome string

const (
	OutcomeAccepted  Outcome = "accepted"
	OutcomeRetryable Outcome = "retryable"
	OutcomePermanent Outcome = "permanent"
)

// Message is the admin API shape; bodies are never part of it. Nil pointers serialise as null.
type Message struct {
	ID                string     `json:"id"`
	RequestID         string     `json:"request_id"`
	App               string     `json:"app"`
	Channel           Channel    `json:"channel"`
	Recipient         string     `json:"recipient"`
	Sender            string     `json:"sender"`
	State             State      `json:"state"`
	HoldReason        *string    `json:"hold_reason"`
	Attempts          int        `json:"attempts"`
	ErrorType         *ErrorType `json:"error_type"`
	Provider          *string    `json:"provider"`
	ProviderMessageID *string    `json:"provider_message_id"`
	BodyBytes         int        `json:"body_bytes"`
	CreatedAt         time.Time  `json:"created_at"`
	SentAt            *time.Time `json:"sent_at"`
	FinalAt           *time.Time `json:"final_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	NextAttemptAt     *time.Time `json:"next_attempt_at"`
}

type Attempt struct {
	N              int        `json:"n"`
	StartedAt      time.Time  `json:"started_at"`
	DurationMS     int64      `json:"duration_ms"`
	Outcome        Outcome    `json:"outcome"`
	ErrorType      *ErrorType `json:"error_type"`
	ProviderStatus *int       `json:"provider_status"`
	ProviderError  *string    `json:"provider_error"`
	Message        string     `json:"message"`
}
