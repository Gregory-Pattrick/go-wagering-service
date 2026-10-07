"""Bounded multi-process correctness checks; this is not a throughput benchmark."""
import concurrent.futures
import json
import os
from pathlib import Path
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from datetime import datetime, timezone

sys.path.insert(0, '/scripts')
from common import require, sqs_client

BASES = os.environ['API_URLS'].split(',')
REPORT = {'startedAt': datetime.now(timezone.utc).isoformat(), 'status': 'RUNNING',
          'instances': BASES, 'scenarios': [], 'wallets': [], 'httpResponses': {}}
COUNTS_LOCK = threading.Lock()
INBOX_IDS = []
EXPECTED_WALLETS = {}


def identity():
    return str(uuid.uuid4())


def request(base, method, path, token=None, body=None, key=None):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if key:
        headers['Idempotency-Key'] = key
    req = urllib.request.Request(base + path, method=method, headers=headers,
                                 data=None if body is None else json.dumps(body).encode())
    try:
        response = urllib.request.urlopen(req, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        status, result = response.status, json.load(response)
    with COUNTS_LOCK:
        counts = REPORT['httpResponses'].setdefault(base, {})
        counts[str(status)] = counts.get(str(status), 0) + 1
    return status, result


def access_token(client, secret):
    body = urllib.parse.urlencode({'grant_type': 'client_credentials',
                                  'client_id': client, 'client_secret': secret}).encode()
    req = urllib.request.Request(os.environ['KEYCLOAK_TOKEN_URL'], data=body)
    with urllib.request.urlopen(req, timeout=15) as response:
        return json.load(response)['access_token']


def wait(check, description, seconds=90):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(0.25)
    raise RuntimeError('Timed out: ' + description)


def healthy(base):
    try:
        return request(base, 'GET', '/health/live')[0] == 200
    except (OSError, ValueError):
        return False


def parallel(jobs):
    # One thread per job: all callers reach the barrier before any is released.
    barrier = threading.Barrier(len(jobs), timeout=20)
    def start(job):
        barrier.wait()
        return job()
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(jobs)) as pool:
        return list(pool.map(start, jobs))


def opened(internal):
    player = identity()
    status, wallet = request(BASES[0], 'POST', '/wallets', internal,
                            {'playerId': player, 'initialBalance': {'amount': '100.00', 'currency': 'BRL'}})
    require(status == 201, 'Wallet creation failed: HTTP ' + str(status))
    REPORT['wallets'].append(wallet['id'])
    return player, wallet['id']


def operation(player, wallet, kind='BET', amount='80.00', reference=None):
    body = {'providerId': 'provider-a', 'externalTransactionId': identity(),
            'playerId': player, 'walletId': wallet, 'roundId': 'distributed-round',
            'gameId': 'distributed-game', 'kind': kind,
            'money': {'amount': amount, 'currency': 'BRL'}}
    if reference:
        body['referenceExternalTransactionId'] = reference
    return body


def submit(index, provider, body):
    return request(BASES[index % 3], 'POST', '/wagering/transactions', provider,
                   body, 'provider-a:' + body['externalTransactionId'])


def verify_wallet(internal, wallet, amount, version):
    EXPECTED_WALLETS[wallet] = (amount, version)
    # Read independently through every process after the concurrent phase ends.
    for base in BASES:
        status, current = request(base, 'GET', '/wallets/' + wallet, internal)
        require(status == 200 and current['balance']['amount'] == amount and current['version'] == version,
                'Final balance/version mismatch at ' + base)
        status, check = request(base, 'POST', '/wallets/' + wallet + '/reconciliation', internal)
        require(status == 200 and check['consistent'] and check['difference']['amount'] == '0.00'
                and check['checkedEntries'] == version, 'Reconciliation mismatch at ' + base)


def passed(name):
    REPORT['scenarios'].append(name)
    print('PASS: ' + name, flush=True)


def run():
    require(len(BASES) == 3 and len(set(BASES)) == 3, 'Exactly three distinct API addresses required')
    for base in BASES:
        wait(lambda: healthy(base), 'API startup: ' + base)
    internal = access_token('wallet-service', os.environ['WALLET_CLIENT_SECRET'])
    provider = access_token('provider-a', os.environ['PROVIDER_A_CLIENT_SECRET'])

    # Three simultaneous independent debits can afford only one winner.
    player, wallet = opened(internal)
    bodies = [operation(player, wallet) for _ in range(3)]
    results = parallel([lambda i=i: submit(i, provider, bodies[i]) for i in range(3)])
    require(sorted(status for status, _ in results) == [200, 422, 422], 'Overspending status mismatch')
    require(sum(r['status'] == 'PROCESSED' for _, r in results) == 1, 'Expected exactly one debit')
    require(all(r.get('failureCode') == 'INSUFFICIENT_FUNDS' for s, r in results if s == 422),
            'Unexpected rejection reason')
    verify_wallet(internal, wallet, '20.00', 2)
    passed('three-instance overspending race')

    player, wallet = opened(internal)
    body = operation(player, wallet)
    duplicate_requests = 50
    results = parallel([lambda i=i: submit(i, provider, body) for i in range(duplicate_requests)])
    require(len(results) == duplicate_requests, 'Missing duplicate responses')
    require(all(s == 200 and r['balance']['amount'] == '20.00' for s, r in results), 'Duplicate failed')
    require(len({r['transactionId'] for _, r in results}) == 1, 'Duplicate transaction identity')
    require(sum(not r['idempotentReplay'] for _, r in results) == 1, 'Expected one original response')
    status, _ = submit(1, provider, operation(player, wallet, 'WIN', '30.00'))
    require(status == 200, 'WIN failed')
    for i in range(3):
        status, replay = submit(i, provider, body)
        require(status == 200 and replay['idempotentReplay'] and replay['balance']['amount'] == '20.00',
                'Replay lost original snapshot')
    verify_wallet(internal, wallet, '50.00', 3)
    REPORT['duplicateRequests'] = duplicate_requests
    REPORT['duplicateOriginalResponses'] = sum(not r['idempotentReplay'] for _, r in results)
    REPORT['duplicateReplayResponses'] = sum(r['idempotentReplay'] for _, r in results)
    passed('50 concurrent requests across three APIs and immutable replay snapshot')

    player, wallet = opened(internal)
    bet = operation(player, wallet)
    require(submit(0, provider, bet)[0] == 200, 'Reference BET failed')
    reverse = [operation(player, wallet, kind, reference=bet['externalTransactionId'])
               for kind in ['REFUND', 'ROLLBACK']]
    results = parallel([lambda i=i: submit(i + 1, provider, reverse[i]) for i in range(2)])
    require(sorted(s for s, _ in results) == [200, 422], 'Double compensation accepted')
    require(next(r for s, r in results if s == 422)['failureCode'] == 'ALREADY_REVERSED',
            'Unexpected compensation rejection')
    verify_wallet(internal, wallet, '100.00', 3)
    passed('REFUND versus ROLLBACK across processes')

    credentials = json.loads(Path('/credentials/producer.json').read_text())
    producer = sqs_client(credentials['accessKeyId'], credentials['secretAccessKey'])
    queue = os.environ['SQS_INPUT_QUEUE_URL']
    # Multiple wallets allow two consumers to process distinct FIFO groups.
    fixtures = []
    for _ in range(4):
        player, wallet = opened(internal)
        body = operation(player, wallet)
        envelope = {'messageId': identity(), 'type': 'WagerTransactionRequested',
                    'occurredAt': datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z'),
                    'data': dict(body, idempotencyKey='provider-a:' + body['externalTransactionId'])}
        fixtures.append((body, envelope))
        INBOX_IDS.append(envelope['messageId'])

    def send(envelope):
        # Fresh transport IDs bypass FIFO's short-lived deduplication cache.
        return producer.send_message(QueueUrl=queue, MessageBody=json.dumps(envelope),
                                     MessageGroupId=envelope['data']['walletId'],
                                     MessageDeduplicationId=identity())
    jobs = []
    for i, (body, envelope) in enumerate(fixtures):
        jobs.extend([lambda e=envelope: send(e), lambda b=body, i=i: submit(i, provider, b)])
    mixed_results = parallel(jobs)
    require(all(result[0] == 200 for result in mixed_results[1::2]),
            'Mixed transport HTTP submission failed')
    # Repeat identical envelopes, then distinct envelopes with the same financial identity.
    for body, envelope in fixtures:
        send(envelope)
        second = dict(envelope, messageId=identity())
        INBOX_IDS.append(second['messageId'])
        send(second)
        def complete(body=body):
            status, result = request(BASES[2], 'GET', '/providers/provider-a/wagering/transactions/'
                                     + body['externalTransactionId'], provider)
            return status == 200 and result['status'] == 'PROCESSED'
        wait(complete, 'Mixed transport commit')
        verify_wallet(internal, body['walletId'], '20.00', 2)
    passed('mixed HTTP/SQS identities; inbox completion requires the SQL audit')

    # Generate only canonical UUID literals, never request-derived SQL fragments.
    ids = ','.join("'" + str(uuid.UUID(value)) + "'" for value in INBOX_IDS)
    Path('/results/inbox.sql').write_text(f"""DO $$
DECLARE n integer;
BEGIN
 FOR attempt IN 1..180 LOOP
  SELECT count(*) INTO n FROM wagering.inbox
   WHERE consumer_name='wager-transactions-v1' AND message_id IN ({ids});
  IF n={len(INBOX_IDS)} THEN RETURN; END IF;
  PERFORM pg_sleep(0.5);
 END LOOP;
 RAISE EXCEPTION 'Expected {len(INBOX_IDS)} committed inbox envelopes, found %', n;
END $$;
""")
    # Recheck expected outcomes after every SQS envelope has committed, not only
    # while duplicate deliveries may still be in flight.
    with Path('/results/inbox.sql').open('a') as assertions:
        for wallet, (amount, version) in EXPECTED_WALLETS.items():
            major, minor = amount.split('.')
            cents = int(major) * 100 + int(minor)
            wallet = str(uuid.UUID(wallet))
            assertions.write(f"""DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM wagering.wallets WHERE id='{wallet}'::uuid
               AND balance_minor={cents} AND version={int(version)})
 THEN RAISE EXCEPTION 'Unexpected final fixture balance/version'; END IF;
END $$;
""")
    REPORT['expectedInboxEnvelopes'] = len(INBOX_IDS)
    REPORT['status'] = 'API_PASS_SQL_AUDIT_PENDING'


if __name__ == '__main__':
    try:
        # Prevent a failed run from accidentally reusing an earlier inbox manifest.
        Path('/results/inbox.sql').write_text("DO $$ BEGIN RAISE EXCEPTION 'Runner did not complete'; END $$;\n")
        run()
    except Exception as error:
        REPORT['status'] = 'FAILED'
        REPORT['errorType'] = type(error).__name__
        raise
    finally:
        REPORT['finishedAt'] = datetime.now(timezone.utc).isoformat()
        Path('/results/report.json').write_text(json.dumps(REPORT, indent=2) + '\n')
