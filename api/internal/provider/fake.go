package provider

import (
	"context"
	"sync"

	"github.com/davcs86/commsgun/api/internal/model"
)

// Fake is an in-memory provider for tests; like Resend it is idempotent by message id.
type Fake struct {
	mu       sync.Mutex
	sent     []model.Message
	accepted map[string]string
	script   []SendResult
	statuses map[string]DeliveryStatus
}

func NewFake() *Fake {
	return &Fake{accepted: map[string]string{}, statuses: map[string]DeliveryStatus{}}
}

func (f *Fake) Name() string { return "fake" }

func (f *Fake) Script(results ...SendResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = append(f.script, results...)
}

func (f *Fake) SetStatus(id string, s DeliveryStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[id] = s
}

func (f *Fake) Sent() []model.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Message(nil), f.sent...)
}

func (f *Fake) Send(_ context.Context, m model.Message, _ Body) SendResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.accepted[m.ID]; ok {
		return SendResult{Outcome: Accepted, ProviderID: id}
	}
	res := SendResult{Outcome: Accepted}
	if len(f.script) > 0 {
		res, f.script = f.script[0], f.script[1:]
	}
	if res.Outcome == Accepted {
		if res.ProviderID == "" {
			res.ProviderID = "fake-" + m.ID
		}
		f.accepted[m.ID] = res.ProviderID
		f.sent = append(f.sent, m)
	}
	return res
}

func (f *Fake) Status(_ context.Context, providerID string) (DeliveryStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statuses[providerID], nil
}
