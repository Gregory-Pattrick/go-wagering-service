package sqs

import (
	"context"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability/tracing"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func traceMessageAttributes(ctx context.Context) map[string]types.MessageAttributeValue {
	result := map[string]types.MessageAttributeValue{}
	for k, v := range tracing.Carrier(ctx) {
		if v != "" {
			result[k] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(v)}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
func readTraceAttributes(input map[string]types.MessageAttributeValue) map[string]string {
	result := map[string]string{}
	for _, key := range []string{"traceparent", "tracestate"} {
		if v, ok := input[key]; ok && aws.ToString(v.DataType) == "String" {
			value := aws.ToString(v.StringValue)
			if len(value) <= 512 {
				result[key] = value
			}
		}
	}
	return result
}
func (h *RequestHandler) WithTracer(t financial.TracePort) delivery.Handler {
	return NewRequestHandler(func() (financial.InboxBackend, error) {
		b, e := h.source()
		if e != nil {
			return nil, e
		}
		return financial.TraceInbox(b, t), nil
	}, providerNames(h.providers), h.logger)
}
func providerNames(providers map[string]bool) []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	return names
}
