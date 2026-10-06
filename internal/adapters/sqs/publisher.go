// Package sqs implements SQS transport using the official AWS SDK for Go v2.
package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
)

type SendAPI interface {
	SendMessage(context.Context, *awssqs.SendMessageInput, ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error)
}
type Publisher struct {
	client SendAPI
	queue  string
}

func NewWithClient(client SendAPI, queue string) *Publisher {
	return &Publisher{client: client, queue: queue}
}

// Load only the dedicated publisher key. No ambient administrator credential
// chain or metadata-service fallback is used by this local worker process.
func NewPublisher(c config.PublisherConfig) (*Publisher, error) {
	file, err := os.Open(c.CredentialsFile)
	if err != nil {
		return nil, errors.New("open SQS publisher credentials file")
	}
	defer file.Close()
	var key struct {
		AccessKeyID     string `json:"accessKeyId"`
		SecretAccessKey string `json:"secretAccessKey"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 8193))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&key); err != nil || key.AccessKeyID == "" || key.SecretAccessKey == "" {
		return nil, errors.New("invalid SQS publisher credentials file")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, errors.New("invalid SQS publisher credentials trailing data")
	}
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: key.AccessKeyID, SecretAccessKey: key.SecretAccessKey, Source: "publisher-file"}, nil
	})
	client := awssqs.New(awssqs.Options{
		Region: c.Region, BaseEndpoint: aws.String(c.Endpoint), Credentials: aws.NewCredentialsCache(provider),
		HTTPClient: &http.Client{Timeout: 8 * time.Second}, RetryMaxAttempts: 1,
	})
	return NewWithClient(client, c.QueueURL), nil
}
func (p *Publisher) Send(ctx context.Context, event delivery.Event) error {
	_, err := p.client.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl: aws.String(p.queue), MessageBody: aws.String(string(event.Payload)),
		MessageGroupId: aws.String(event.AggregateID), MessageDeduplicationId: aws.String(event.ID),
		MessageAttributes: traceMessageAttributes(ctx),
	})
	return err
}

var _ delivery.Sender = (*Publisher)(nil)
