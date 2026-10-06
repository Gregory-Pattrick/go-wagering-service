package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability"
	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type AttributesAPI interface {
	GetQueueAttributes(context.Context, *awssqs.GetQueueAttributesInput, ...func(*awssqs.Options)) (*awssqs.GetQueueAttributesOutput, error)
}
type Monitor struct{ client AttributesAPI }

func NewMonitor(c config.TelemetryConfig) (observability.QueueProbe, error) {
	data, err := os.ReadFile(c.CredentialsFile)
	if err != nil || len(data) > 8192 {
		return nil, errors.New("read monitor credentials")
	}
	var key struct {
		AccessKeyID     string `json:"accessKeyId"`
		SecretAccessKey string `json:"secretAccessKey"`
	}
	if err = json.Unmarshal(data, &key); err != nil || key.AccessKeyID == "" || key.SecretAccessKey == "" {
		return nil, errors.New("invalid monitor credentials")
	}
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: key.AccessKeyID, SecretAccessKey: key.SecretAccessKey, Source: "monitor-file"}, nil
	})
	client := awssqs.New(awssqs.Options{Region: c.Region, BaseEndpoint: aws.String(c.Endpoint), Credentials: aws.NewCredentialsCache(provider), HTTPClient: &http.Client{Timeout: 2500 * time.Millisecond}, RetryMaxAttempts: 1})
	return &Monitor{client}, nil
}
func (m *Monitor) Inspect(ctx context.Context, queue string) (observability.QueueDepth, error) {
	result, err := m.client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{QueueUrl: aws.String(queue), AttributeNames: []types.QueueAttributeName{"FifoQueue", "ApproximateNumberOfMessages", "ApproximateNumberOfMessagesNotVisible", "ApproximateNumberOfMessagesDelayed"}})
	if err != nil {
		return observability.QueueDepth{}, err
	}
	if result.Attributes["FifoQueue"] != "true" {
		return observability.QueueDepth{}, errors.New("monitored queue is not FIFO")
	}
	names := []string{"ApproximateNumberOfMessages", "ApproximateNumberOfMessagesNotVisible", "ApproximateNumberOfMessagesDelayed"}
	var values [3]float64
	for i, name := range names {
		n, err := strconv.ParseUint(result.Attributes[name], 10, 64)
		if err != nil {
			return observability.QueueDepth{}, errors.New("invalid queue depth")
		}
		values[i] = float64(n)
	}
	return observability.QueueDepth{Visible: values[0], Inflight: values[1], Delayed: values[2]}, nil
}
