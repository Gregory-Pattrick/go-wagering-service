package financial

import (
	"errors"
	"testing"
)

type countingObserver struct {
	completed, failed int
	status            string
	replay            bool
}

func (o *countingObserver) FinancialCompleted(_, status string, replay bool) {
	o.completed++
	o.status = status
	o.replay = replay
}
func (o *countingObserver) FinancialError(string, string) { o.failed++ }
func TestObservationWaitsForCommitAndPreservesError(t *testing.T) {
	injected := errors.New("commit failed")
	for _, commitErr := range []error{nil, injected} {
		o := &countingObserver{}
		err := ObserveSQL(o, "http", func(callback func(Unit) error) error {
			if err := callback(nil); err != nil {
				return err
			}
			return commitErr
		}, func(u Unit) error {
			observed := u.(*observedUnit)
			observed.status = "PROCESSED"
			observed.replay = true
			return nil
		})
		if !errors.Is(err, commitErr) {
			t.Fatal("observer changed result")
		}
		if commitErr == nil && (o.completed != 1 || !o.replay || o.status != "PROCESSED") {
			t.Fatal("missing committed outcome")
		}
		if commitErr != nil && (o.completed != 0 || o.failed != 1) {
			t.Fatal("counted an unconfirmed outcome")
		}
	}
}
