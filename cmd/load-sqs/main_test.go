package main

import (
	"encoding/hex"
	adapter "github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/sqs"
	"testing"
	"time"
)

func TestNearestRankPercentiles(t *testing.T) {
	values := []float64{10, 1, 8, 3, 5, 2, 6, 4, 9, 7}
	if percentile(values, .5) != 5 || percentile(values, .95) != 10 || percentile(values, .99) != 10 {
		t.Fatal("unexpected nearest-rank percentile")
	}
	if values[0] != 10 {
		t.Fatal("percentile mutated raw observations")
	}
}
func TestIdentifiersAreDistinctUUIDs(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := identifier()
		if len(id) != 36 || id[14] != '4' || seen[id] {
			t.Fatal("invalid or duplicate identifier")
		}
		seen[id] = true
		raw := id[:8] + id[9:13] + id[14:18] + id[19:23] + id[24:]
		b, e := hex.DecodeString(raw)
		if e != nil || b[8]&0xc0 != 0x80 {
			t.Fatal("invalid UUID variant")
		}
	}
}
func TestGeneratorBounds(t *testing.T) {
	for _, raw := range []string{"0", "-1", "101", "bad"} {
		t.Setenv("SQS_LOAD_RATE", raw)
		if _, e := integer("SQS_LOAD_RATE", 20, 100); e == nil {
			t.Fatal("accepted invalid rate")
		}
	}
	t.Setenv("SQS_LOAD_RATE", "")
	v, e := integer("SQS_LOAD_RATE", 20, 100)
	if e != nil || v != 20 {
		t.Fatal("default rate")
	}
}

func TestEnvelopeMatchesConsumerContract(t *testing.T) {
	w := wallet{ID: identifier(), Player: identifier()}
	external := "sqs-load-" + identifier() + "-10"
	now := time.Now()
	first, e := requestEnvelope(w, external, identifier(), now)
	if e != nil {
		t.Fatal(e)
	}
	second, e := requestEnvelope(w, external, identifier(), now)
	if e != nil {
		t.Fatal(e)
	}
	a, e := adapter.DecodeRequest(first)
	if e != nil {
		t.Fatal(e)
	}
	b, e := adapter.DecodeRequest(second)
	if e != nil {
		t.Fatal(e)
	}
	if a.MessageID == b.MessageID || a.Input.IdempotencyKey != b.Input.IdempotencyKey || a.Input.WalletID != w.ID {
		t.Fatal("duplicate envelope identities do not match consumer contract")
	}
}
