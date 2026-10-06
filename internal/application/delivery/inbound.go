package delivery

import (
	"context"
	"time"
)

type Message struct {
	DeliveryID, Receipt, GroupID string
	Body                         []byte
	TraceContext                 map[string]string
	ReceiveCount                 int
}
type Queue interface {
	Receive(context.Context) (*Message, error)
	Delete(context.Context, Message) error
	Release(context.Context, Message, time.Duration) error
}
type Handler interface {
	Handle(context.Context, Message) error
}
