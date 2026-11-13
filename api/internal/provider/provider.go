// Package provider defines the send/status contract and its implementations.
package provider

import (
	"context"
	"regexp"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

type Outcome int

const (
	Accepted Outcome = iota
	Retryable
	Permanent
	Throttled
)

const (
	PauseRateLimit    = "rate_limit"
	PauseDailyQuota   = "daily_quota"
	PauseMonthlyQuota = "monthly_quota"
)

type SendResult struct {
	Outcome               Outcome
	ProviderID            string
	ErrorType             model.ErrorType
	Status                int
	ProviderError         string
	Message               string
	PauseFor              time.Duration
	PauseUntilUTCMidnight bool
	PauseReason           string
}

type DeliveryStatus int

const (
	Pending DeliveryStatus = iota
	Delivered
	Bounced
	Complained
	Rejected
)

type Body struct{ Subject, Text, HTML, ReplyTo, SMSBody string }

type Provider interface {
	Name() string
	Send(ctx context.Context, m model.Message, body Body) SendResult
}

type StatusChecker interface {
	Status(ctx context.Context, providerID string) (DeliveryStatus, error)
}

var addressPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*`)

// safeMessage strips email addresses, then truncates on a rune boundary.
func safeMessage(s string) string {
	s = addressPattern.ReplaceAllString(s, "[redacted]")
	if len(s) <= constants.AttemptMessageMaxLen {
		return s
	}
	cut := constants.AttemptMessageMaxLen
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
