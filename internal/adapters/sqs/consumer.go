package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type ConsumerAPI interface {
	ReceiveMessage(context.Context, *awssqs.ReceiveMessageInput, ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *awssqs.DeleteMessageInput, ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(context.Context, *awssqs.ChangeMessageVisibilityInput, ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error)
}
type InputQueue struct {
	client ConsumerAPI
	url    string
}

func NewInputQueue(c config.ConsumerConfig) (*InputQueue, error) {
	data, err := os.ReadFile(c.CredentialsFile)
	if err != nil || len(data) > 8192 {
		return nil, errors.New("read SQS consumer credentials file")
	}
	var key struct {
		AccessKeyID     string `json:"accessKeyId"`
		SecretAccessKey string `json:"secretAccessKey"`
	}
	if err = json.Unmarshal(data, &key); err != nil || key.AccessKeyID == "" || key.SecretAccessKey == "" {
		return nil, errors.New("invalid SQS consumer credentials")
	}
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: key.AccessKeyID, SecretAccessKey: key.SecretAccessKey, Source: "consumer-file"}, nil
	})
	client := awssqs.New(awssqs.Options{Region: c.Region, BaseEndpoint: aws.String(c.Endpoint), Credentials: aws.NewCredentialsCache(provider), HTTPClient: &http.Client{Timeout: 14 * time.Second}, RetryMaxAttempts: 1})
	return &InputQueue{client: client, url: c.QueueURL}, nil
}
func (q *InputQueue) Receive(ctx context.Context) (*delivery.Message, error) {
	result, err := q.client.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl: aws.String(q.url), MaxNumberOfMessages: 1, WaitTimeSeconds: 10, VisibilityTimeout: 30,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{"ApproximateReceiveCount", "MessageGroupId"},
	})
	if err != nil {
		return nil, err
	}
	if len(result.Messages) == 0 {
		return nil, nil
	}
	msg := result.Messages[0]
	count, err := strconv.Atoi(msg.Attributes["ApproximateReceiveCount"])
	if err != nil || count < 1 {
		return nil, errors.New("invalid SQS receive count")
	}
	return &delivery.Message{DeliveryID: aws.ToString(msg.MessageId), Receipt: aws.ToString(msg.ReceiptHandle), GroupID: msg.Attributes["MessageGroupId"], Body: []byte(aws.ToString(msg.Body)), ReceiveCount: count}, nil
}
func (q *InputQueue) Delete(ctx context.Context, message delivery.Message) error {
	_, err := q.client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(q.url), ReceiptHandle: aws.String(message.Receipt)})
	return err
}
func (q *InputQueue) Release(ctx context.Context, message delivery.Message, delay time.Duration) error {
	seconds := int32((delay + time.Second - 1) / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	if seconds > 60 {
		seconds = 60
	}
	_, err := q.client.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(q.url), ReceiptHandle: aws.String(message.Receipt), VisibilityTimeout: seconds})
	return err
}

var _ delivery.Queue = (*InputQueue)(nil)
