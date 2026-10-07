# Multi-process correctness suite

This suite starts three independent API processes, two worker processes (each
runs an outbox publisher and a reference worker), and two SQS consumers. All
seven Go processes use `-race`, `halt_on_error=1`, and `restart: no`.

Use the standalone `compose.distributed.yaml` with project name
`wagering-distributed`. PostgreSQL, MiniStack, Keycloak, credentials and result
volumes belong to this project. No host ports are published. This suite does
not use or reset the development database. Migration startup is a prerequisite
for every Go process. The image is a test image, not the production runtime.

## Run on Windows

From the repository root, open `scripts/test-distributed.ps1` in your editor.
If script execution is allowed, run it directly. Otherwise execute the whole file as one script block:

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\test-distributed.ps1")))
```

Do not run individual selections from its try/finally.

The script builds the race image, starts dependencies and processes, runs the
HTTP/SQS scenarios, runs the SQL audit, inspects the seven processes, captures
evidence in `test-results/distributed-<timestamp>`, and stops the isolated stack.
It preserves volumes. Each invocation creates new fixture identities.
Do not execute this suite concurrently with another invocation of itself.

There are no new Go dependencies in this block. Earlier AWS SDK and Prometheus
dependency updates must already be applied. The first image build can be slow.

## Scenarios and assertions

| Scenario | Expected result |
| --- | --- |
| Three concurrent BETs of 80.00 on 100.00 | One PROCESSED, two INSUFFICIENT_FUNDS, balance 20.00, version 2 |
| Fifty identical requests distributed across three APIs | One original response and forty-nine replays; one transaction ID |
| Replay after a WIN of 30.00 | Original BET response remains 20.00; current balance is 50.00 |
| Concurrent REFUND and ROLLBACK of the same BET | One compensation, one ALREADY_REVERSED, final balance 100.00 |
| Four wallets submitted through HTTP and SQS concurrently | One debit per financial identity |
| Same and different envelope IDs, fresh SQS transport deduplication IDs | Eight distinct completed inbox rows; duplicates do not move money |
| SQL audit after delivery | Wallet = ledger = liability account; every journal balances; identities unique; outbox drains |

Requests target each API's unique Compose DNS name directly, rather than an
unverifiable load-balancer distribution. The report records response counts by
address. Caller barriers coordinate request launch; they do not guarantee a
specific order of SQL statements or prove that every interleaving occurred.

The mixed scenario first checks HTTP-visible results. Its report deliberately
uses `API_PASS_SQL_AUDIT_PENDING`; the subsequent SQL audit proves that every
expected envelope actually committed to the inbox. Approximate queue depth is
not used as that proof. Only the script's final PASS completes the whole suite.

The two publishers and consumers are running concurrently. This suite does
not prove fair work distribution between them, precise crash-window recovery,
or exactly-once SQS delivery. The outbox audit verifies publication acknowledgments,
not an independent output consumer's receipt. Dedicated interruption tests are
still required. The known MiniStack signature-validation limitation remains.

## Evidence and interpretation

`report.json` includes actual timestamps, scenario outcomes, fixture wallet IDs,
and HTTP response counts. `containers.txt` and `processes.log` identify the
processes and preserve failures. `result.txt` exists only after all assertions
and process checks pass. A failed assertion must be investigated, not converted
into a pass or silently retried with different financial identities.

No RPS, performance percentile, or capacity claim is derived from this suite.
Race instrumentation changes performance, and this is a bounded correctness
exercise. Load measurements and deterministic process-interruption tests remain
separate delivery gates. Do not commit runtime logs, tokens, or generated reports
without reviewing their contents; retain only deliberately sanitized evidence.

## Manual recovery

If the terminal closes before cleanup, stop only the isolated project:

```powershell
docker compose -p wagering-distributed -f compose.distributed.yaml stop
```

Do not add `-v` to cleanup commands. Repeated runs retain prior test records;
the SQL invariant checks cover them too. If a previous run left poison input,
inspect it before rerunning instead of purging the queue.
