package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

const defaultResendURL = "https://api.resend.com"

// errorNamePattern admits only identifier-like provider error names, so no address can reach a log.
var errorNamePattern = regexp.MustCompile(`^[a-z0-9_]+$`)

type Resend struct {
	apiKey  string
	hc      *http.Client
	baseURL string
}

func NewResend(apiKey string, hc *http.Client, baseURL string) *Resend {
	if hc == nil {
		hc = &http.Client{}
	}
	if baseURL == "" {
		baseURL = defaultResendURL
	}
	return &Resend{apiKey: apiKey, hc: hc, baseURL: strings.TrimRight(baseURL, "/")}
}

func (r *Resend) Name() string { return "resend" }

type sendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text,omitempty"`
	HTML    string   `json:"html,omitempty"`
	ReplyTo string   `json:"reply_to,omitempty"`
}

type apiError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

func (r *Resend) Send(ctx context.Context, m model.Message, body Body) SendResult {
	payload, err := json.Marshal(sendRequest{
		From: m.Sender, To: []string{m.Recipient}, Subject: body.Subject,
		Text: body.Text, HTML: body.HTML, ReplyTo: body.ReplyTo,
	})
	if err != nil {
		return SendResult{Outcome: Permanent, ErrorType: model.ErrUnknown, Message: "could not encode request"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/emails", bytes.NewReader(payload))
	if err != nil {
		return SendResult{Outcome: Permanent, ErrorType: model.ErrUnknown, Message: "could not build request"}
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", m.ID)

	resp, err := r.hc.Do(req)
	if err != nil {
		if isTimeout(ctx, err) {
			return SendResult{Outcome: Retryable, ErrorType: model.ErrTimeout, Message: "request timed out"}
		}
		return SendResult{Outcome: Retryable, ErrorType: model.ErrNetwork, Message: "network error"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, constants.ProviderResponseMaxBytes+1))
	if err != nil {
		if isTimeout(ctx, err) {
			return SendResult{Outcome: Retryable, ErrorType: model.ErrTimeout, Status: resp.StatusCode, Message: "response timed out"}
		}
		return SendResult{Outcome: Retryable, ErrorType: model.ErrNetwork, Status: resp.StatusCode, Message: "network error reading response"}
	}
	if len(raw) > constants.ProviderResponseMaxBytes {
		raw = nil
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var ok struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &ok) != nil || ok.ID == "" {
			return SendResult{Outcome: Retryable, ErrorType: model.ErrProviderUnavailable, Status: resp.StatusCode, Message: "2xx response without an id"}
		}
		return SendResult{Outcome: Accepted, ProviderID: ok.ID, Status: resp.StatusCode}
	}

	var ae apiError
	_ = json.Unmarshal(raw, &ae)
	res := classify(resp.StatusCode, ae)
	res.Status = resp.StatusCode
	res.ProviderError = ae.Name
	res.Message = safeMessage(ae.Message)
	if res.Outcome == Throttled && res.PauseReason == PauseRateLimit {
		res.PauseFor = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return res
}

// authPause holds the queue rather than failing it: a bad key is the operator's to fix, not the message's.
var authPause = SendResult{Outcome: Throttled, ErrorType: model.ErrAuth, PauseFor: constants.AuthPause, PauseReason: PauseAuth}

func classify(status int, ae apiError) SendResult {
	retry := SendResult{Outcome: Retryable, ErrorType: model.ErrProviderUnavailable}
	perm := func(t model.ErrorType) SendResult { return SendResult{Outcome: Permanent, ErrorType: t} }
	switch {
	case status == http.StatusTooManyRequests:
		switch ae.Name {
		case "daily_quota_exceeded":
			return SendResult{Outcome: Throttled, PauseUntilUTCMidnight: true, PauseReason: PauseDailyQuota}
		case "monthly_quota_exceeded":
			return SendResult{Outcome: Throttled, PauseFor: constants.MonthlyQuotaPause, PauseReason: PauseMonthlyQuota}
		}
		return SendResult{Outcome: Throttled, PauseReason: PauseRateLimit}
	case status == http.StatusRequestTimeout, status >= 500:
		return retry
	case status == http.StatusConflict:
		switch ae.Name {
		case "concurrent_idempotent_requests":
			return retry
		case "invalid_idempotent_request":
			return perm(model.ErrProviderRejected)
		}
	case status == http.StatusUnauthorized:
		return authPause
	case status == http.StatusForbidden:
		if strings.HasSuffix(ae.Name, "_api_key") {
			return authPause
		}
		if ae.Name == "validation_error" && strings.Contains(strings.ToLower(ae.Message), "domain is not verified") {
			return perm(model.ErrSenderNotVerified)
		}
	case status == http.StatusUnprocessableEntity, status == http.StatusBadRequest:
		if namesToField(ae.Message) {
			return perm(model.ErrInvalidRecipient)
		}
		return perm(model.ErrProviderRejected)
	}
	return perm(model.ErrUnknown)
}

func namesToField(msg string) bool {
	l := strings.ToLower(msg)
	for _, s := range []string{"`to`", "'to'", `"to"`, "to field"} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func parseRetryAfter(v string) time.Duration {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		return constants.RateLimitDefaultPause
	}
	if n > int64(constants.RateLimitMaxPause/time.Second) {
		return constants.RateLimitMaxPause
	}
	return time.Duration(n) * time.Second
}

func isTimeout(ctx context.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (r *Resend) Status(ctx context.Context, providerID string) (DeliveryStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/emails/"+url.PathEscape(providerID), nil)
	if err != nil {
		return Pending, errors.New("could not build status request")
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	resp, err := r.hc.Do(req)
	if err != nil {
		return Pending, errors.New("status request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, constants.ProviderResponseMaxBytes))
	if err != nil {
		return Pending, errors.New("status response unreadable")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ae apiError
		_ = json.Unmarshal(raw, &ae)
		if !errorNamePattern.MatchString(ae.Name) || len(ae.Name) > constants.ProviderErrorNameMaxLen {
			return Pending, fmt.Errorf("status http %d", resp.StatusCode)
		}
		return Pending, fmt.Errorf("status http %d %s", resp.StatusCode, ae.Name)
	}
	var out struct {
		LastEvent string `json:"last_event"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return Pending, errors.New("status response not json")
	}
	return mapLastEvent(out.LastEvent), nil
}

func mapLastEvent(ev string) DeliveryStatus {
	switch strings.ToLower(strings.TrimSpace(ev)) {
	case "delivered":
		return Delivered
	case "bounced":
		return Bounced
	case "complained":
		return Complained
	case "failed", "suppressed", "canceled":
		return Rejected
	}
	return Pending
}
