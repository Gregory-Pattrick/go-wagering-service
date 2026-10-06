// Package workers runs cancellable loops over durable database work.
package workers

import (
	"math/rand/v2"
	"time"
)

// Backoff applies equal jitter to exponential delays, bounded by base and cap.
// The minimum remains one second with the production defaults (1s, 60s).
func Backoff(attempt int, base, cap time.Duration) time.Duration {
	ceiling := base
	for n := 1; n < attempt && ceiling < cap; n++ {
		if ceiling > cap/2 {
			ceiling = cap
			break
		}
		ceiling *= 2
	}
	if ceiling > cap {
		ceiling = cap
	}
	floor := ceiling / 2
	if floor < base {
		floor = base
	}
	if ceiling <= floor {
		return floor
	}
	return floor + time.Duration(rand.Int64N(int64(ceiling-floor)+1))
}
