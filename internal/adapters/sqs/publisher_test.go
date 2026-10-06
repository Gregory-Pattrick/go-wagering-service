package sqs

import (
	"context"
	"errors"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"testing"
)

type captureClient struct {
	calls []*awssqs.SendMessageInput
	err   error
}

func (c *captureClient) SendMessage(_ context.Context, input *awssqs.SendMessageInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
	c.calls = append(c.calls, input)
	return &awssqs.SendMessageOutput{}, c.err
}
func TestRetryKeepsStableFIFOIdentityAndSnapshot(t *testing.T) {
	client := &captureClient{err: errors.New("transport failure")}
	p := NewWithClient(client, "queue.fifo")
	event := delivery.Event{ID: "event-id", AggregateID: "wallet-id", Payload: []byte(`{"amount":"20.00"}`)}
	if err := p.Send(context.Background(), event); err == nil {
		t.Fatal("transport failure hidden")
	}
	client.err = nil
	if err := p.Send(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	for _, call := range client.calls {
		if aws.ToString(call.MessageDeduplicationId) != event.ID || aws.ToString(call.MessageGroupId) != event.AggregateID || aws.ToString(call.MessageBody) != string(event.Payload) {
			t.Fatal("changed delivery identity or snapshot")
		}
	}
}
