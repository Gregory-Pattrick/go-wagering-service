# Performance evidence and provenance

These are historical measurements supplied by the author. They predate the
broker signature gate and the delivery-readiness changes. They are not a PASS
report for the amended delivery revision or measurements of signature overhead.

| Run | Supplied base revision | Working tree | Files |
| --- | --- | --- | --- |
| HTTP | ff8c3653db3c09d0fa997fa8f736e63de3ca4793 | Dirty; exact paths retained in metadata | [Report](http-historical/PERFORMANCE.md), [environment](http-historical/environment.json) |
| SQS | 219a7f0a71d3dd613f9a54761fa82cbc0db24eae | Dirty; exact paths retained in metadata | [Report](sqs-historical/SQS-PERFORMANCE.md), [environment](sqs-historical/environment.json) |

The base revision alone cannot reproduce uncommitted file contents. The metadata
lists changed paths but does not preserve their complete diff. Neither supplied
metadata file records an exact run timestamp or exact command invocation; those
values are intentionally not invented. Raw k6 results, SQL audit output and full
runtime reports are not included in these four supplied files. Preserve the
original evidence directories separately.

## Reading the results

- HTTP many-wallet workload: offered 20 iterations/s, completed 19.958 requests
  per configured second, p95 54.596216 ms and p99 about 464.061216 ms. Six
  iterations were dropped. The report records an unexpected-response rate of 0.
- SQS many-wallet workload: 2,400 accepted envelopes and 2,400 unique movements;
  observed terminal p95 330.471 ms and p99 6,554.046 ms. The supplied summaries
  do not establish the cause of that tail latency.
- The SQS publication-outage case reports an observed outbox drain of 12.164 s
  after publishers restart. It does not measure broker crash durability.

Each run has one repetition. Offered rate is not maximum capacity. HTTP hot-wallet
latency includes expected business rejections. SQS terminal latency includes
HTTP polling and scheduling delay and is an upper bound on commit latency.
Read each original report's scope and methodology before comparing numbers.

## Integrity and privacy

[provenance.json](provenance.json) records original and packaged SHA-256 hashes.
Only UTF-8 BOM removal if present and line-ending normalization are permitted;
all supplied values, revisions and dirty-file metadata are retained. These are
selected reports and machine metadata, without bearer tokens, runtime access
keys, raw logs or full transaction payloads. Hashes check file integrity, not the
truth of a measurement.

## Final revision measurements

After code validation, commit and push before rerunning performance suites.
Record the exact tested revision and actual working-tree state. Put new reports
in a separate directory; never overwrite or relabel these historical runs.
Follow [the delivery checklist](../DELIVERY-CHECKLIST.md) and
[final validation](../FINAL-VALIDATION.md). A new run is required if claiming that
performance figures describe the signature-validating delivery revision.
