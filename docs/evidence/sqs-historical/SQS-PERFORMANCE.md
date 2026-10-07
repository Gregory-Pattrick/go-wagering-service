# Measured SQS performance

| Case | Offered msg/s | Accepted/configured s | Envelopes | Unique movements | Generator drops | Send p50/p95/p99 ms | Observed terminal p50/p95/p99 ms | Terminal drain s | Peak sampled input | Peak sampled outbox | Peak sampled outbox age s | Outbox drain observed s |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| many | 20 | 20 | 2400 | 2400 | 0 | 3.362/14.300/21.416 | 194.012/330.471/6554.046 | 0.202 | 29.0 | 19.0 | 0.493134 | 3.078 |
| duplicates | 20 | 20 | 2400 | 1200 | 0 | 4.785/19.256/25.361 | 195.520/330.806/496.037 | 11.776 | 20.0 | 10.0 | 0.481008 | 2.066 |
| outage | 20 | 20 | 400 | 400 | 0 | 4.278/18.325/23.101 | 193.251/328.545/352.438 | 0.201 | 2.0 | 840.0 | 30.405315 | 12.164 |

- many: 126 samples; surfaced API database-conflict delta 0. Consumer conflicts are not exposed by this API counter.
- duplicates: 136 samples; surfaced API database-conflict delta 0. Consumer conflicts are not exposed by this API counter.
- outage: 33 samples; surfaced API database-conflict delta 0. Consumer conflicts are not exposed by this API counter.

Send latency measures one SDK SendMessage call. Observed terminal latency measures from the first successful send attempt start for a business identity to its first PROCESSED HTTP observation.
Terminal latency includes polling and observer scheduling delay; it is an upper bound on commit latency. It excludes duplicate-envelope completion latency, which is verified separately by SQL.
All envelopes use unique transport deduplication IDs and unique inbox message IDs. The duplicates case reuses business identity in adjacent pairs.
The serial producer offers fixed slots, skips missed slots and never bursts to catch up. Throughput divides accepted envelopes by configured duration.
Observer concurrency is 16 with 250 ms between rounds. Its HTTP requests share API and database resources with processing.
Terminal drain starts at the last send return. Queue drain uses fresh approximate depths; SQL independently requires all accepted envelopes in the inbox.
All monetary operations are WIN 0.01 BRL. Unique movement counts are compared against exact expected balances, versions and reconciled entries through every API.
Only the outage case pauses both publishers. Consumers and APIs remain active. Outbox drain is sampled after publishers restart; controller startup and probe resolution affect the measurement.
Two normal Go consumers, three APIs and two publishers; tracing disabled. No race instrumentation in measured binaries; producer unit tests run with -race during image build.
Warmup results are separate; many/duplicates use 30 s warmup + 120 s measurement. The outage case uses one 20 s measured window without warmup.
One run is not a capacity estimate or a security certification; the documented MiniStack signature limitation remains.
