package sqs

import (
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
)

func (h *RequestHandler) WithObserver(observer financial.Observer) delivery.Handler {
	return &RequestHandler{providers: h.providers, source: func() (financial.InboxBackend, error) {
		backend, err := h.source()
		if err != nil {
			return nil, err
		}
		return financial.ObserveInbox(backend, observer), nil
	}}
}
