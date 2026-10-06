// Package delivery defines transport-independent committed-event delivery ports.
package delivery

import (
	"context"
	"errors"
	"time"
)

var ErrLeaseLost = errors.New("durable lease lost")

type Event struct {
	ID, AggregateID, Owner string
	Payload                []byte
	Attempts               int
}
type Outbox interface {
	ClaimEvent(context.Context, string, time.Duration) (Event, error)
	ConfirmEvent(context.Context, Event) error
	RetryEvent(context.Context, Event, time.Duration) error
}
type Sender interface {
	Send(context.Context, Event) error
}

type ReferenceWork struct {
	ID, Owner string
	Attempts  int
}
type References interface {
	ClaimReference(context.Context, string, time.Duration) (ReferenceWork, error)
	ResumeReference(context.Context, ReferenceWork, time.Duration, time.Duration) (string, error)
	RetryReference(context.Context, ReferenceWork, time.Duration) error
}
