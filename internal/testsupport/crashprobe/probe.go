//go:build faultinjection

// Package crashprobe is compiled only into the fault-injection test executable.
package crashprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/workers"
)

type Target struct {
	TransactionID string `json:"transactionId"`
	EnvelopeID    string `json:"envelopeId"`
}
type Record struct {
	Point         string    `json:"point"`
	Instance      string    `json:"instance"`
	ID            string    `json:"id"`
	EnvelopeID    string    `json:"envelopeId,omitempty"`
	Owner         string    `json:"owner,omitempty"`
	PayloadSHA256 string    `json:"payloadSHA256,omitempty"`
	Attempt       int       `json:"attempt"`
	At            time.Time `json:"at"`
}
type Probe struct {
	Directory, Instance, Point string
	Target                     Target
	// Halt is injectable only in unit tests. The executable installs the bounded
	// process barrier below; no request can release it through HTTP or SQS.
	Halt func()
}

func New(directory, instance, point string) (*Probe, error) {
	if directory == "" || instance == "" || filepath.Base(instance) != instance {
		return nil, errors.New("invalid probe path or instance")
	}
	switch point {
	case "observe", "before-send", "after-send", "before-delete", "after-reference-claim":
	default:
		return nil, errors.New("unknown fault point")
	}
	data, err := os.ReadFile(filepath.Join(directory, "target.json"))
	if err != nil {
		return nil, err
	}
	var target Target
	if err = json.Unmarshal(data, &target); err != nil {
		return nil, err
	}
	if target.TransactionID == "" && target.EnvelopeID == "" {
		return nil, errors.New("empty probe target")
	}
	return &Probe{Directory: directory, Instance: instance, Point: point, Target: target, Halt: func() {
		// Ignore operation cancellation deliberately: return would move execution
		// past the exact boundary being tested. The host must send SIGKILL.
		<-time.After(3 * time.Minute)
		panic("fault-injection barrier timed out without SIGKILL")
	}}, nil
}
func (p *Probe) Write(suffix string, r Record) error {
	r.Instance = p.Instance
	r.At = time.Now().UTC()
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(p.Directory, ".probe-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Probe files are private test metadata read by a root test observer.
	return os.Rename(name, filepath.Join(p.Directory, p.Instance+"-"+suffix+".json"))
}
func (p *Probe) hit(point string, r Record) error {
	if p.Point != point {
		return nil
	}
	r.Point = point
	if err := p.Write("blocked", r); err != nil {
		return err
	}
	p.Halt()
	return nil
}
func fingerprint(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
func (p *Probe) event(e delivery.Event) (Record, bool) {
	var header struct {
		Type string `json:"eventType"`
		Data struct {
			TransactionID string `json:"transactionId"`
		} `json:"data"`
	}
	if json.Unmarshal(e.Payload, &header) != nil {
		return Record{}, false
	}
	return Record{ID: e.ID, Owner: e.Owner, PayloadSHA256: fingerprint(e.Payload), Attempt: e.Attempts},
		header.Type == "WagerTransactionProcessed" && header.Data.TransactionID == p.Target.TransactionID
}
func (p *Probe) message(m delivery.Message) (Record, bool) {
	var header struct {
		MessageID string `json:"messageId"`
	}
	if json.Unmarshal(m.Body, &header) != nil {
		return Record{}, false
	}
	return Record{ID: m.DeliveryID, EnvelopeID: header.MessageID, PayloadSHA256: fingerprint(m.Body), Attempt: m.ReceiveCount},
		header.MessageID == p.Target.EnvelopeID
}

type Sender struct {
	Next  delivery.Sender
	Probe *Probe
}

func (s Sender) Send(ctx context.Context, e delivery.Event) error {
	r, target := s.Probe.event(e)
	if target {
		if err := s.Probe.hit("before-send", r); err != nil {
			return err
		}
	}
	if err := s.Next.Send(ctx, e); err != nil {
		return err
	}
	if target {
		r.Point = "sent"
		if err := s.Probe.Write("sent", r); err != nil {
			return err
		}
		return s.Probe.hit("after-send", r)
	}
	return nil
}

type Backend struct {
	workers.Backend
	Probe *Probe
}

func (b Backend) ConfirmEvent(ctx context.Context, e delivery.Event) error {
	if err := b.Backend.ConfirmEvent(ctx, e); err != nil {
		return err
	}
	if r, target := b.Probe.event(e); target {
		r.Point = "confirmed"
		return b.Probe.Write("confirmed", r)
	}
	return nil
}
func (b Backend) ResumeReference(ctx context.Context, w delivery.ReferenceWork, delay, ttl time.Duration) (string, error) {
	if w.ID == b.Probe.Target.TransactionID {
		r := Record{ID: w.ID, Owner: w.Owner, Attempt: w.Attempts}
		if err := b.Probe.hit("after-reference-claim", r); err != nil {
			return "", err
		}
	}
	status, err := b.Backend.ResumeReference(ctx, w, delay, ttl)
	if err == nil && status == "PROCESSED" && w.ID == b.Probe.Target.TransactionID {
		err = b.Probe.Write("reference", Record{Point: "reference-processed", ID: w.ID, Owner: w.Owner, Attempt: w.Attempts})
	}
	return status, err
}

type Queue struct {
	delivery.Queue
	Probe *Probe
}

func (q Queue) Delete(ctx context.Context, m delivery.Message) error {
	r, target := q.Probe.message(m)
	if target {
		if err := q.Probe.hit("before-delete", r); err != nil {
			return err
		}
	}
	if err := q.Queue.Delete(ctx, m); err != nil {
		return err
	}
	if target {
		r.Point = "deleted"
		return q.Probe.Write("deleted", r)
	}
	return nil
}
