// Command load-sqs generates bounded local SQS load; it is not an application role.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
)

var bases = []string{"http://api-1:8080", "http://api-2:8080", "http://api-3:8080"}
var transport = &http.Client{Timeout: 5 * time.Second}

type wallet struct {
	ID     string `json:"id"`
	Player string `json:"playerId"`
}
type money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}
type result struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Balance    money  `json:"balance"`
	Version    int64  `json:"version"`
	Consistent bool   `json:"consistent"`
	Difference money  `json:"difference"`
	Checked    int64  `json:"checkedEntries"`
}
type tokenValue struct {
	value string
	at    time.Time
}

var tokenMu sync.Mutex
var tokens = map[string]tokenValue{}

func token(client string) (string, error) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if saved := tokens[client]; saved.value != "" && time.Since(saved.at) < 60*time.Second {
		return saved.value, nil
	}
	secret := os.Getenv("PROVIDER_A_CLIENT_SECRET")
	if client == "wallet-service" {
		secret = os.Getenv("WALLET_CLIENT_SECRET")
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {client}, "client_secret": {secret}}
	response, err := transport.Post("http://keycloak:8080/realms/wagering/protocol/openid-connect/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("token transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("token HTTP %d", response.StatusCode)
	}
	var decoded struct {
		Access string `json:"access_token"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&decoded)
	if err != nil || decoded.Access == "" {
		return "", errors.New("invalid token response")
	}
	tokens[client] = tokenValue{decoded.Access, time.Now()}
	return decoded.Access, nil
}
func api(index int, method, path, client string, body any) (int, result, error) {
	access, err := token(client)
	if err != nil {
		return 0, result{}, err
	}
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return 0, result{}, err
		}
	}
	request, err := http.NewRequest(method, bases[index%len(bases)]+path, bytes.NewReader(payload))
	if err != nil {
		return 0, result{}, err
	}
	request.Header.Set("Authorization", "Bearer "+access)
	request.Header.Set("Content-Type", "application/json")
	response, err := transport.Do(request)
	if err != nil {
		return 0, result{}, errors.New("API transport failed")
	}
	defer response.Body.Close()
	var decoded result
	err = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&decoded)
	return response.StatusCode, decoded, err
}
func identifier() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(math.Ceil(p*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
func distribution(values []float64) map[string]float64 {
	return map[string]float64{"p50": percentile(values, .50), "p95": percentile(values, .95), "p99": percentile(values, .99)}
}
func integer(name string, fallback, maximum int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	v, e := strconv.Atoi(raw)
	if e != nil || v < 1 || v > maximum {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return v, nil
}

type observation struct {
	External   string    `json:"externalId"`
	Wallet     string    `json:"walletId"`
	Start      time.Time `json:"-"`
	ObservedMS float64   `json:"observedTerminalMs"`
	Error      string    `json:"error,omitempty"`
}
type envelopeRecord struct {
	Message  string  `json:"messageId"`
	External string  `json:"externalId"`
	SendMS   float64 `json:"sendMs"`
}
type report struct {
	Status               string             `json:"status"`
	Case                 string             `json:"case"`
	Phase                string             `json:"phase"`
	Rate                 int                `json:"offeredMessagesPerSecond"`
	Seconds              int                `json:"durationSeconds"`
	Scheduled            int                `json:"scheduledSlots"`
	Dropped              int                `json:"generatorDropped"`
	SendErrors           int                `json:"sendErrors"`
	PollErrors           int                `json:"pollErrors"`
	AcceptedPerSecond    float64            `json:"acceptedPerConfiguredSecond"`
	TerminalDrainSeconds float64            `json:"observedTerminalDrainSeconds"`
	SendPercentiles      map[string]float64 `json:"sendLatencyMs"`
	TerminalPercentiles  map[string]float64 `json:"observedTerminalLatencyMs"`
	Envelopes            []envelopeRecord   `json:"envelopes"`
	Operations           []*observation     `json:"operations"`
	Wallets              []wallet           `json:"wallets"`
	Failure              string             `json:"failure,omitempty"`
}

func sender() (*awssqs.Client, error) {
	raw, err := os.ReadFile("/credentials/producer.json")
	if err != nil {
		return nil, errors.New("producer credentials unavailable")
	}
	var key struct {
		Access string `json:"accessKeyId"`
		Secret string `json:"secretAccessKey"`
	}
	if json.Unmarshal(raw, &key) != nil || key.Access == "" || key.Secret == "" {
		return nil, errors.New("invalid producer credentials")
	}
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: key.Access, SecretAccessKey: key.Secret, Source: "load-producer-file"}, nil
	})
	return awssqs.New(awssqs.Options{Region: "us-east-1", BaseEndpoint: aws.String("http://ministack:4566"), Credentials: aws.NewCredentialsCache(provider), HTTPClient: &http.Client{Timeout: 8 * time.Second}, RetryMaxAttempts: 1}), nil
}
func requestEnvelope(w wallet, external, message string, now time.Time) ([]byte, error) {
	data := map[string]any{"providerId": "provider-a", "externalTransactionId": external, "idempotencyKey": "provider-a:" + external, "playerId": w.Player, "walletId": w.ID, "roundId": "sqs-load", "gameId": "sqs-load", "kind": "WIN", "money": money{"0.01", "BRL"}}
	return json.Marshal(map[string]any{"messageId": message, "type": "WagerTransactionRequested", "occurredAt": now.UTC().Format(time.RFC3339Nano), "data": data})
}
func exercise(r *report) error {
	client, err := sender()
	if err != nil {
		return err
	}
	for i := 0; i < 20; i++ {
		player := identifier()
		code, w, e := api(i, "POST", "/wallets", "wallet-service", map[string]any{"playerId": player, "initialBalance": money{"100.00", "BRL"}})
		if e != nil || code != 201 {
			return fmt.Errorf("open wallet failed: HTTP %d", code)
		}
		r.Wallets = append(r.Wallets, wallet{w.ID, player})
	}
	// Independent, bounded observer. Its polling/API scheduling delay is included in observed latency.
	var mu sync.Mutex
	unique := map[string]*observation{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			mu.Lock()
			pending := []*observation{}
			for _, o := range unique {
				if o.ObservedMS == 0 && o.Error == "" {
					pending = append(pending, o)
				}
			}
			mu.Unlock()
			var wg sync.WaitGroup
			slots := make(chan struct{}, 16)
			for i, o := range pending {
				if ctx.Err() != nil {
					break
				}
				slots <- struct{}{}
				wg.Add(1)
				go func(index int, entry *observation) {
					defer wg.Done()
					defer func() { <-slots }()
					code, v, e := api(index, "GET", "/providers/provider-a/wagering/transactions/"+entry.External, "provider-a", nil)
					mu.Lock()
					defer mu.Unlock()
					if e != nil || (code != 200 && code != 404) {
						r.PollErrors++
						return
					}
					if code == 200 {
						if v.Status == "PROCESSED" {
							entry.ObservedMS = float64(time.Since(entry.Start).Microseconds()) / 1000
						} else {
							entry.Error = "unexpected terminal/status: " + v.Status
						}
					}
				}(i, o)
			}
			wg.Wait()
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	defer func() { cancel(); <-done }()
	start := time.Now()
	r.Scheduled = r.Rate * r.Seconds
	// Fixed slots, no catch-up burst. Serial SendMessage is an explicit generator constraint.
	for slot := 0; slot < r.Scheduled; slot++ {
		due := start.Add(time.Duration(float64(slot) / float64(r.Rate) * float64(time.Second)))
		if delay := time.Until(due); delay > 0 {
			time.Sleep(delay)
		}
		if time.Since(due) > time.Second/time.Duration(r.Rate) {
			r.Dropped++
			continue
		}
		w := r.Wallets[(slot/2)%len(r.Wallets)]
		external := identifier()
		if r.Case == "duplicates" {
			external = fmt.Sprintf("sqs-load-%s-%d", r.Wallets[0].ID, slot/2)
		}
		message := identifier()
		payload, e := requestEnvelope(w, external, message, time.Now())
		if e != nil {
			return e
		}
		began := time.Now()
		sendContext, stop := context.WithTimeout(context.Background(), 8*time.Second)
		_, e = client.SendMessage(sendContext, &awssqs.SendMessageInput{QueueUrl: aws.String("http://ministack:4566/000000000000/wager-transactions.fifo"), MessageBody: aws.String(string(payload)), MessageGroupId: aws.String(w.ID), MessageDeduplicationId: aws.String(message)})
		stop()
		if e != nil {
			r.SendErrors++
			continue
		}
		r.Envelopes = append(r.Envelopes, envelopeRecord{message, external, float64(time.Since(began).Microseconds()) / 1000})
		mu.Lock()
		if _, exists := unique[external]; !exists {
			unique[external] = &observation{External: external, Wallet: w.ID, Start: began}
		}
		mu.Unlock()
	}
	sendEnd := time.Now()
	deadline := sendEnd.Add(180 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		remaining := 0
		for _, o := range unique {
			if o.ObservedMS == 0 && o.Error == "" {
				remaining++
			}
		}
		mu.Unlock()
		if remaining == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.TerminalDrainSeconds = time.Since(sendEnd).Seconds()
	cancel()
	<-done
	for _, o := range unique {
		r.Operations = append(r.Operations, o)
	}
	sort.Slice(r.Operations, func(i, j int) bool { return r.Operations[i].External < r.Operations[j].External })
	sends := []float64{}
	for _, e := range r.Envelopes {
		sends = append(sends, e.SendMS)
	}
	observed := []float64{}
	counts := map[string]int64{}
	for _, o := range r.Operations {
		if o.ObservedMS == 0 || o.Error != "" {
			return errors.New("operation not observed as PROCESSED before deadline")
		}
		observed = append(observed, o.ObservedMS)
		counts[o.Wallet]++
	}
	r.SendPercentiles = distribution(sends)
	r.TerminalPercentiles = distribution(observed)
	r.AcceptedPerSecond = float64(len(r.Envelopes)) / float64(r.Seconds)
	if r.SendErrors != 0 || r.PollErrors != 0 || len(r.Envelopes) == 0 {
		return errors.New("send/poll errors or empty workload; inspect counters")
	}
	if r.Case == "duplicates" && len(r.Envelopes) <= len(r.Operations) {
		return errors.New("duplicate fixture did not reach repeated business identities")
	}
	for _, w := range r.Wallets {
		expected := int64(10000) + counts[w.ID]
		amount := fmt.Sprintf("%d.%02d", expected/100, expected%100)
		for index := range bases {
			code, v, e := api(index, "GET", "/wallets/"+w.ID, "wallet-service", nil)
			if e != nil || code != 200 || v.Balance.Amount != amount || v.Version != counts[w.ID]+1 {
				return errors.New("post-SQS balance/version mismatch")
			}
			code, c, e := api(index, "POST", "/wallets/"+w.ID+"/reconciliation", "wallet-service", nil)
			if e != nil || code != 200 || !c.Consistent || c.Difference.Amount != "0.00" || c.Checked != counts[w.ID]+1 {
				return errors.New("post-SQS reconciliation failed")
			}
		}
	}
	return nil
}
func save(r *report) error {
	prefix := filepath.Join("/results", r.Case+"-"+r.Phase)
	raw, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(prefix+".json", raw, 0644); e != nil {
		return e
	}
	if r.Status != "PASS" {
		return nil
	}
	// IDs are locally generated UUIDs; no external strings are interpolated into SQL.
	var sql strings.Builder
	sql.WriteString("CREATE TEMP TABLE load_expected_envelopes(message_id text PRIMARY KEY);\nINSERT INTO load_expected_envelopes VALUES\n")
	for i, item := range r.Envelopes {
		if i > 0 {
			sql.WriteString(",\n")
		}
		fmt.Fprintf(&sql, "('%s')", item.Message)
	}
	sql.WriteString(";\nDO $$ BEGIN FOR attempt IN 1..180 LOOP IF NOT EXISTS(SELECT 1 FROM load_expected_envelopes e WHERE NOT EXISTS(SELECT 1 FROM wagering.inbox i WHERE i.consumer_name='wager-transactions-v1' AND i.message_id=e.message_id)) THEN RETURN; END IF; PERFORM pg_sleep(0.5); END LOOP; RAISE EXCEPTION 'Not all accepted envelopes completed inbox'; END $$;\n")
	return os.WriteFile(prefix+"-inbox.sql", []byte(sql.String()), 0644)
}
func main() {
	r := &report{Status: "FAIL", Case: os.Getenv("SQS_LOAD_CASE"), Phase: os.Getenv("SQS_LOAD_PHASE")}
	if r.Case != "many" && r.Case != "duplicates" && r.Case != "outage" {
		fmt.Fprintln(os.Stderr, "invalid SQS_LOAD_CASE")
		os.Exit(1)
	}
	if r.Phase != "warmup" && r.Phase != "measure" {
		fmt.Fprintln(os.Stderr, "invalid SQS_LOAD_PHASE")
		os.Exit(1)
	}
	var err error
	r.Rate, err = integer("SQS_LOAD_RATE", 20, 100)
	if err == nil {
		r.Seconds, err = integer("SQS_LOAD_SECONDS", 120, 180)
	}
	if err == nil {
		err = exercise(r)
	}
	if err == nil {
		r.Status = "PASS"
	} else {
		r.Failure = err.Error()
	}
	if e := save(r); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("PASS: SQS %s/%s, %d accepted envelopes, %d unique operations; SQL inbox audit pending\n", r.Case, r.Phase, len(r.Envelopes), len(r.Operations))
}
