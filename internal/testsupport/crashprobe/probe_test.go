//go:build faultinjection

package crashprobe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/workers"
)

type sendFunc func(context.Context, delivery.Event) error

func (f sendFunc) Send(c context.Context, e delivery.Event) error { return f(c, e) }

type queueFake struct {
	delivery.Queue
	deleted bool
}

func (q *queueFake) Delete(context.Context, delivery.Message) error { q.deleted = true; return nil }

func fixture(t *testing.T, point string) *Probe {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.json"), []byte(`{"transactionId":"tx-1","envelopeId":"envelope-1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := New(dir, "fixture", point)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func targetEvent() delivery.Event {
	return delivery.Event{ID: "event-1", Owner: "owner-1", Attempts: 1, Payload: []byte(`{"eventType":"WagerTransactionProcessed","data":{"transactionId":"tx-1"}}`)}
}

func TestSenderBarriersBracketSuccessfulSend(t *testing.T) {
	for _, point := range []string{"before-send", "after-send"} {
		t.Run(point, func(t *testing.T) {
			p := fixture(t, point)
			sent := false
			halted := false
			p.Halt = func() {
				halted = true
				if sent != (point == "after-send") {
					t.Fatal("barrier on wrong side of send")
				}
				if _, err := os.Stat(filepath.Join(p.Directory, "fixture-blocked.json")); err != nil {
					t.Fatal("barrier visible before halt:", err)
				}
			}
			sender := Sender{Next: sendFunc(func(context.Context, delivery.Event) error { sent = true; return nil }), Probe: p}
			if err := sender.Send(context.Background(), targetEvent()); err != nil {
				t.Fatal(err)
			}
			if !sent || !halted {
				t.Fatal("expected both send and test halt")
			}
		})
	}
}
func TestFailedSendNeverSignalsAfterSend(t *testing.T) {
	p := fixture(t, "after-send")
	p.Halt = func() { t.Fatal("failed send reached barrier") }
	expected := errors.New("network failure")
	sender := Sender{Next: sendFunc(func(context.Context, delivery.Event) error { return expected }), Probe: p}
	if !errors.Is(sender.Send(context.Background(), targetEvent()), expected) {
		t.Fatal("error changed")
	}
	if _, err := os.Stat(filepath.Join(p.Directory, "fixture-sent.json")); !os.IsNotExist(err) {
		t.Fatal("failed send recorded as successful")
	}
}
func TestDeleteBarrierPrecedesTransportAcknowledgment(t *testing.T) {
	p := fixture(t, "before-delete")
	next := &queueFake{}
	p.Halt = func() {
		if next.deleted {
			t.Fatal("delete occurred before barrier")
		}
	}
	queue := Queue{Queue: next, Probe: p}
	message := delivery.Message{DeliveryID: "delivery-1", Body: []byte(`{"messageId":"envelope-1"}`), ReceiveCount: 2}
	if err := queue.Delete(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if !next.deleted {
		t.Fatal("delete missing after test release")
	}
}
func TestUntargetedEventDoesNotBlock(t *testing.T) {
	p := fixture(t, "before-send")
	p.Halt = func() { t.Fatal("unrelated event blocked") }
	e := targetEvent()
	e.Payload = []byte(`{"eventType":"WagerTransactionProcessed","data":{"transactionId":"other"}}`)
	sender := Sender{Next: sendFunc(func(context.Context, delivery.Event) error { return nil }), Probe: p}
	if err := sender.Send(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

type backendFake struct {
	workers.Backend
	confirmErr error
}

func (b backendFake) ConfirmEvent(context.Context, delivery.Event) error { return b.confirmErr }
func TestFailedConfirmationDoesNotRecordAcknowledgment(t *testing.T) {
	p := fixture(t, "observe")
	expected := errors.New("lease lost")
	backend := Backend{Backend: backendFake{confirmErr: expected}, Probe: p}
	if !errors.Is(backend.ConfirmEvent(context.Background(), targetEvent()), expected) {
		t.Fatal("confirmation error changed")
	}
	if _, err := os.Stat(filepath.Join(p.Directory, "fixture-confirmed.json")); !os.IsNotExist(err) {
		t.Fatal("failed confirmation recorded as success")
	}
}
