# Measured HTTP performance

Generated from this run; see environment.json and raw k6 JSON files.

| Case | Offered iterations/s | Completed requests / configured second | p50 ms | p95 ms | p99 ms | Unexpected rate | Expected rejections | Replays | Unique movements | Dropped iterations | Peak sampled outbox | Peak sampled age s | Drain observed s |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| many | 20 | 19.958 | 12.96318 | 54.596216 | 464.0612162799988 | 0 | 0 | 0 | 2395 | 6 | 130.0 | 3.250085 | 3.068 |
| hot | 20 | 20.0 | 7.666632 | 24.581613299999994 | 37.93538460999977 | 0 | 2300 | 0 | 100 | 0 | 7.0 | 0.355846 | 1.055 |
| replay | 20 | 20.008 | 3.75522 | 14.450722 | 19.414455 | 0 | 0 | 2401 | 1 | 0 | 0.0 | 0.0 | 1.041 |

- many: 121 metric samples; surfaced database-conflict counter delta 0.
- hot: 119 metric samples; surfaced database-conflict counter delta 0.
- replay: 120 metric samples; surfaced database-conflict counter delta 0.

Latency covers the transaction HTTP request (including expected 422 responses), excluding token acquisition and setup/audit calls.
Throughput divides completed transaction attempts by configured arrival duration; late completions during the 30-second grace period remain counted.
Each iteration sends one measured transaction request. Dropped iterations are reported, not hidden or counted as server failures.
Outbox values are shared database snapshots from API 1 only. Peak values are sampled bounds, not guaranteed instantaneous maxima.
Conflict deltas span the first and last phase samples and only include surfaced errors, not internal SQL retries or pure lock wait.
Drain timing starts after k6 teardown and is limited by probe/poll resolution; it is not per-event publication latency.
Warmups use separate wallets and are excluded from this table. Replay setup creates one original transaction; measured requests must all be replays.
Normal Go build, tracing disabled, metrics enabled, three APIs and two publishers; generator and services share Docker host resources.
SQS load, async end-to-end latency and controlled publication-outage load recovery are not measured by this HTTP block.
No minimum RPS was specified by the challenge. This run measures the configured workload, not maximum system capacity.
