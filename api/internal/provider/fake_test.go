package provider

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/davcs86/commsgun/api/internal/model"
)

func TestFakeDefaultsAndIdempotency(t *testing.T) {
	f := NewFake()
	if f.Name() != "fake" {
		t.Fatalf("name %q", f.Name())
	}
	m := model.Message{ID: "m1"}
	r := f.Send(context.Background(), m, Body{})
	if r.Outcome != Accepted || r.ProviderID != "fake-m1" {
		t.Fatalf("got %+v", r)
	}
	r2 := f.Send(context.Background(), m, Body{})
	if r2.Outcome != Accepted || r2.ProviderID != "fake-m1" {
		t.Fatalf("got %+v", r2)
	}
	if got := f.Sent(); len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("sent %+v", got)
	}
}

func TestFakeScriptInOrderThenAccepts(t *testing.T) {
	f := NewFake()
	f.Script(SendResult{Outcome: Retryable, ErrorType: model.ErrNetwork}, SendResult{Outcome: Permanent, ErrorType: model.ErrAuth})
	m := model.Message{ID: "m2"}
	if r := f.Send(context.Background(), m, Body{}); r.Outcome != Retryable {
		t.Fatalf("1: %+v", r)
	}
	if r := f.Send(context.Background(), m, Body{}); r.Outcome != Permanent {
		t.Fatalf("2: %+v", r)
	}
	if len(f.Sent()) != 0 {
		t.Fatal("failed sends must not be recorded")
	}
	if r := f.Send(context.Background(), m, Body{}); r.Outcome != Accepted || r.ProviderID != "fake-m2" {
		t.Fatalf("3: %+v", r)
	}
	if len(f.Sent()) != 1 {
		t.Fatal("want one sent")
	}
}

func TestFakeStatus(t *testing.T) {
	f := NewFake()
	if s, err := f.Status(context.Background(), "x"); err != nil || s != Pending {
		t.Fatalf("%v %v", s, err)
	}
	f.SetStatus("x", Bounced)
	if s, _ := f.Status(context.Background(), "x"); s != Bounced {
		t.Fatalf("%v", s)
	}
}

func TestFakeConcurrent(t *testing.T) {
	f := NewFake()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("m%d", i%10)
			f.Send(context.Background(), model.Message{ID: id}, Body{})
			f.Status(context.Background(), id)
			f.SetStatus(id, Delivered)
			f.Sent()
		}(i)
	}
	wg.Wait()
	if len(f.Sent()) != 10 {
		t.Fatalf("sent %d", len(f.Sent()))
	}
}
