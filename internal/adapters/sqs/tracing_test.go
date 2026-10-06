package sqs

import (
	"context"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability/tracing"
	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"testing"
)

type traceSendCapture struct{ input *awssqs.SendMessageInput }

func (c *traceSendCapture) SendMessage(_ context.Context, in *awssqs.SendMessageInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
	c.input = in
	return &awssqs.SendMessageOutput{}, nil
}
func TestTraceMetadataDoesNotChangeEventPayloadOrFIFOIdentity(t *testing.T) {
	client := &traceSendCapture{}
	p := NewWithClient(client, "http://sqs/events.fifo")
	event := delivery.Event{ID: "event-1", AggregateID: "wallet-1", Payload: []byte(`{"unchanged":true}`)}
	ctx := tracing.Extract(context.Background(), map[string]string{"traceparent": "00-11111111111111111111111111111111-2222222222222222-01"})
	if err := p.Send(ctx, event); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(client.input.MessageBody) != string(event.Payload) || aws.ToString(client.input.MessageDeduplicationId) != event.ID || aws.ToString(client.input.MessageGroupId) != event.AggregateID {
		t.Fatal("tracing changed financial event transport identity")
	}
	received := readTraceAttributes(client.input.MessageAttributes)
	if received["traceparent"] != tracing.Carrier(ctx)["traceparent"] {
		t.Fatal("trace attribute lost")
	}
	if err := p.Send(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(client.input.MessageAttributes) != 0 {
		t.Fatal("disabled tracing added metadata")
	}
}
