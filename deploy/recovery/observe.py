"""Observer for the private recovery project. It never controls Docker itself."""
import hashlib
import json
import os
from pathlib import Path
import sys
import time
import uuid
from datetime import datetime, timezone

sys.path.insert(0, '/distributed')
import run as api

ROOT = Path('/control')
CURRENT = ROOT / 'current'
FIXTURE = CURRENT / 'fixture.json'


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def write(name, value):
    (CURRENT / name).write_text(json.dumps(value, indent=2) + '\n')


def read(name):
    return json.loads((CURRENT / name).read_text())


def tokens():
    return (api.access_token('wallet-service', os.environ['WALLET_CLIENT_SECRET']),
            api.access_token('provider-a', os.environ['PROVIDER_A_CLIENT_SECRET']))


def sql_uuid(value):
    return "'" + str(uuid.UUID(value)) + "'"


def get_transaction(fixture, provider):
    status, result = api.request(api.BASES[0], 'GET',
        '/providers/provider-a/wagering/transactions/' + fixture['body']['externalTransactionId'], provider)
    require(status == 200, 'Transaction query failed')
    return result


def setup(scenario):
    require(scenario in ('consumer', 'before-send', 'after-send', 'reference'), 'Unknown scenario')
    CURRENT.mkdir(parents=True, exist_ok=True)
    os.chown(CURRENT, 65532, 65532)
    os.chmod(CURRENT, 0o755)
    # Remove only previous probe metadata in this private test-control directory.
    # Database, queue contents, credentials and archived evidence are untouched.
    for path in CURRENT.iterdir():
        if path.is_file():
            path.unlink()
    api.wait(lambda: api.healthy(api.BASES[0]), 'API startup')
    internal, provider = tokens()
    player, wallet = api.opened(internal)
    body = api.operation(player, wallet)
    fixture = {'scenario': scenario, 'wallet': wallet, 'player': player, 'body': body,
               'startedAt': datetime.now(timezone.utc).isoformat()}
    target = {'transactionId': '', 'envelopeId': ''}
    if scenario == 'consumer':
        envelope = {'messageId': api.identity(), 'type': 'WagerTransactionRequested',
                    'occurredAt': datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z'),
                    'data': dict(body, idempotencyKey='provider-a:' + body['externalTransactionId'])}
        fixture['envelope'] = envelope
        target['envelopeId'] = envelope['messageId']
    else:
        if scenario == 'reference':
            fixture['futureBet'] = body
            body = api.operation(player, wallet, 'REFUND', reference=body['externalTransactionId'])
            fixture['body'] = body
        status, result = api.submit(0, provider, body)
        expected = 202 if scenario == 'reference' else 200
        require(status == expected, 'Unexpected initial financial response')
        require(result['status'] == ('PENDING_REFERENCE' if scenario == 'reference' else 'PROCESSED'),
                'Unexpected initial transaction status')
        fixture['transactionId'] = result['transactionId']
        target['transactionId'] = result['transactionId']
    write('target.json', target)
    write('fixture.json', fixture)
    print('READY: ' + scenario, flush=True)


def send_input():
    fixture = read('fixture.json')
    credentials = json.loads(Path('/credentials/producer.json').read_text())
    producer = api.sqs_client(credentials['accessKeyId'], credentials['secretAccessKey'])
    response = producer.send_message(QueueUrl=os.environ['SQS_INPUT_QUEUE_URL'],
        MessageBody=json.dumps(fixture['envelope']), MessageGroupId=fixture['wallet'],
        MessageDeduplicationId=api.identity())
    fixture['deliveryId'] = response['MessageId']
    write('fixture.json', fixture)


def wait_blocked():
    api.wait(lambda: (CURRENT / 'fault-blocked.json').exists(), 'observable fault barrier')
    fixture = read('fixture.json')
    marker = read('fault-blocked.json')
    internal, provider = tokens()
    result = get_transaction(fixture, provider)
    fixture['transactionId'] = result['transactionId']
    write('fixture.json', fixture)
    wallet, tx = sql_uuid(fixture['wallet']), sql_uuid(fixture['transactionId'])
    point = {'consumer': 'before-delete', 'reference': 'after-reference-claim'}.get(
        fixture['scenario'], fixture['scenario'])
    require(marker['point'] == point, 'Wrong fault barrier')
    expected_balance, expected_version = (10000, 1) if fixture['scenario'] == 'reference' else (2000, 2)
    assertions = [f"IF NOT EXISTS(SELECT 1 FROM wagering.wallets WHERE id={wallet}::uuid AND balance_minor={expected_balance} AND version={expected_version}) THEN RAISE EXCEPTION 'Checkpoint balance/version mismatch'; END IF;"]
    if fixture['scenario'] == 'consumer':
        require(marker['id'] == fixture['deliveryId'], 'Unexpected broker delivery identity')
        envelope = sql_uuid(fixture['envelope']['messageId'])
        assertions.append(f"IF (SELECT count(*) FROM wagering.inbox WHERE consumer_name='wager-transactions-v1' AND message_id={envelope} AND transaction_id={tx}::uuid)<>1 THEN RAISE EXCEPTION 'Financial/inbox commit absent before delete'; END IF;")
    elif fixture['scenario'] == 'reference':
        require(marker['id'] == fixture['transactionId'], 'Unexpected reference claim')
        owner = sql_uuid(marker['owner'])
        assertions.append(f"IF NOT EXISTS(SELECT 1 FROM wagering.transaction_work WHERE transaction_id={tx}::uuid AND lease_owner={owner} AND attempts={int(marker['attempt'])}) THEN RAISE EXCEPTION 'Reference claim not persisted'; END IF;")
    else:
        event, owner = sql_uuid(marker['id']), sql_uuid(marker['owner'])
        assertions.append(f"IF NOT EXISTS(SELECT 1 FROM wagering.outbox WHERE event_id={event}::uuid AND transaction_id={tx}::uuid AND published_at IS NULL AND lease_owner={owner} AND attempts={int(marker['attempt'])}) THEN RAISE EXCEPTION 'Outbox claim/confirmation boundary mismatch'; END IF;")
    (CURRENT / 'checkpoint.sql').write_text('DO $$ BEGIN\n' + '\n'.join(assertions) + '\nEND $$;\n')
    print('BARRIER: committed state ready for SQL verification before SIGKILL', flush=True)


def observe_output():
    marker = read('fault-blocked.json')
    fixture = read('fixture.json')
    client = api.sqs_client()  # Administrator is limited to this test observer.
    queue = os.environ['SQS_OUTPUT_QUEUE_URL']
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        response = client.receive_message(QueueUrl=queue, MaxNumberOfMessages=10, WaitTimeSeconds=2,
            MessageSystemAttributeNames=['MessageGroupId', 'MessageDeduplicationId'])
        for message in response.get('Messages', []):
            body = json.loads(message['Body'])
            matched = body['eventId'] == marker['id']
            if matched:
                digest = hashlib.sha256(message['Body'].encode()).hexdigest()
                require(digest == marker['payloadSHA256'], 'Broker payload changed')
                attrs = message.get('Attributes', {})
                require(attrs.get('MessageDeduplicationId') == marker['id'], 'Unstable FIFO deduplication ID')
                require(attrs.get('MessageGroupId') == fixture['wallet'], 'Wrong FIFO wallet group')
                write('output-observed.json', {'eventId': body['eventId'], 'payloadSHA256': digest,
                                             'messageId': message['MessageId']})
            # This is the isolated recovery queue. Consumed output fixtures are
            # acknowledged, including earlier runs, so FIFO groups can progress.
            client.delete_message(QueueUrl=queue, ReceiptHandle=message['ReceiptHandle'])
            if matched:
                print('PASS: target event independently observed in SQS', flush=True)
                return
    raise RuntimeError('Target output event was not observed')


def reference_arrives():
    fixture = read('fixture.json')
    _, provider = tokens()
    status, result = api.submit(0, provider, fixture['futureBet'])
    require(status == 200 and result['balance']['amount'] == '20.00', 'Later reference BET failed')


def recovered_marker(fixture):
    names = ['recovery-consumer-deleted.json'] if fixture['scenario'] == 'consumer' else [
        name + ('-reference.json' if fixture['scenario'] == 'reference' else '-confirmed.json')
        for name in ('recovery-a', 'recovery-b')]
    found = []
    def locate():
        for name in names:
            if (CURRENT / name).exists():
                found.append(read(name))
                return True
        return False
    api.wait(locate, 'durable takeover or broker redelivery', seconds=120)
    return found[0]


def verify(after_restart=False):
    fixture, blocked = read('fixture.json'), read('fault-blocked.json')
    recovered = recovered_marker(fixture)
    require(recovered['id'] == blocked['id'], 'Recovery changed stable identity')
    require(recovered['attempt'] > blocked['attempt'], 'No new claim or broker redelivery observed')
    if fixture['scenario'] == 'consumer':
        require(recovered['envelopeId'] == blocked['envelopeId'], 'Envelope identity changed')
    else:
        require(recovered['owner'] != blocked['owner'], 'Recovery reused the failed claim owner')
    if fixture['scenario'] != 'reference':
        require(recovered['payloadSHA256'] == blocked['payloadSHA256'], 'Immutable payload changed')
    api.wait(lambda: api.healthy(api.BASES[0]), 'API restart')
    internal, provider = tokens()
    expected_amount, expected_version = ('100.00', 3) if fixture['scenario'] == 'reference' else ('20.00', 2)
    api.verify_wallet(internal, fixture['wallet'], expected_amount, expected_version)
    status, replay = api.submit(0, provider, fixture['body'])
    require(status == 200 and replay['idempotentReplay'] and replay['transactionId'] == fixture['transactionId']
            and replay['balance']['amount'] == expected_amount, 'Replay after crash/restart failed')
    tx, wallet = sql_uuid(fixture['transactionId']), sql_uuid(fixture['wallet'])
    clauses = [
        f"IF (SELECT count(*) FROM wagering.wallet_ledger_entries WHERE transaction_id={tx}::uuid)<>1 THEN RAISE EXCEPTION 'Missing or duplicated movement'; END IF;",
        f"IF (SELECT count(*) FROM wagering.outbox WHERE transaction_id={tx}::uuid AND event_type='WagerTransactionProcessed')<>1 THEN RAISE EXCEPTION 'Missing or duplicated terminal event'; END IF;",
        f"IF EXISTS(SELECT 1 FROM wagering.transaction_work WHERE transaction_id={tx}::uuid) THEN RAISE EXCEPTION 'Terminal transaction retained reference work'; END IF;",
        f"IF NOT EXISTS(SELECT 1 FROM wagering.wallets WHERE id={wallet}::uuid AND balance_minor={10000 if expected_version==3 else 2000} AND version={expected_version}) THEN RAISE EXCEPTION 'Final balance/version mismatch'; END IF;"
    ]
    if fixture['scenario'] == 'consumer':
        envelope = sql_uuid(fixture['envelope']['messageId'])
        clauses.append(f"IF (SELECT count(*) FROM wagering.inbox WHERE consumer_name='wager-transactions-v1' AND message_id={envelope} AND transaction_id={tx}::uuid)<>1 THEN RAISE EXCEPTION 'Duplicate or missing inbox'; END IF;")
    (CURRENT / 'verify.sql').write_text('DO $$ BEGIN\n'+'\n'.join(clauses)+'\nEND $$;\n')
    write('recovery-observed.json', recovered)
    print('PASS: crash recovery' + (' and process restart' if after_restart else '') + '; SQL audit pending', flush=True)


def archive():
    fixture = read('fixture.json')
    result = {'scenario': fixture['scenario'], 'status': 'PASS_AFTER_SQL_AND_RESTART',
              'startedAt': fixture['startedAt'], 'finishedAt': datetime.now(timezone.utc).isoformat(),
              'walletId': fixture['wallet'], 'transactionId': fixture['transactionId'],
              'blocked': read('fault-blocked.json'), 'recovered': read('recovery-observed.json')}
    if (CURRENT / 'output-observed.json').exists():
        result['output'] = read('output-observed.json')
    results = ROOT / 'results'
    results.mkdir(exist_ok=True)
    (results / (fixture['scenario'] + '.json')).write_text(json.dumps(result, indent=2)+'\n')


if __name__ == '__main__':
    mode = sys.argv[1]
    if mode == 'setup': setup(sys.argv[2])
    elif mode == 'send': send_input()
    elif mode == 'blocked': wait_blocked()
    elif mode == 'output': observe_output()
    elif mode == 'reference': reference_arrives()
    elif mode == 'verify': verify()
    elif mode == 'restart': verify(after_restart=True)
    elif mode == 'archive': archive()
    elif mode == 'export':
        print(json.dumps({str(p.relative_to(ROOT)): json.loads(p.read_text())
                          for p in ROOT.rglob('*.json') if not p.name.startswith('.probe-')}, indent=2))
    else: raise RuntimeError('Unknown observer mode')
