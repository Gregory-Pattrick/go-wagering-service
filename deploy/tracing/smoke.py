"""Check real trace continuity and financial behavior during collector outage."""
import base64
import json
import os
from pathlib import Path
import string
import sys
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone
sys.path.insert(0, '/distributed')
import run as api


def require(condition, message):
    if not condition: raise RuntimeError(message)


def parent():
    trace_id = uuid.uuid4().hex
    return trace_id, '00-' + trace_id + '-' + uuid.uuid4().hex[:16] + '-01'


def submit(provider, body, traceparent):
    req = urllib.request.Request(api.BASES[0] + '/wagering/transactions', method='POST',
        headers={'Authorization': 'Bearer ' + provider, 'Content-Type': 'application/json',
                 'Idempotency-Key': 'provider-a:' + body['externalTransactionId'], 'traceparent': traceparent},
        data=json.dumps(body).encode())
    try: response = urllib.request.urlopen(req, timeout=15)
    except urllib.error.HTTPError as error: response = error
    with response:
        return response.status, json.load(response)


def lookup(body, provider):
    status, result = api.request(api.BASES[0], 'GET', '/providers/provider-a/wagering/transactions/'
                                + body['externalTransactionId'], provider)
    return result if status == 200 else None


def objects(value):
    if isinstance(value, dict):
        yield value
        for nested in value.values(): yield from objects(nested)
    elif isinstance(value, list):
        for nested in value: yield from objects(nested)


def hex_id(value):
    if len(value) in (16, 32) and all(c in string.hexdigits for c in value): return value.lower()
    try: return base64.b64decode(value, validate=True).hex()
    except (ValueError, TypeError): return ''


def check_trace(trace_id, expected, services):
    url = os.environ.get('TEMPO_URL', 'http://tempo:3200') + '/api/traces/' + trace_id
    try:
        req = urllib.request.Request(url, headers={'Accept': 'application/json'})
        with urllib.request.urlopen(req, timeout=5) as response: data = json.load(response)
    except (OSError, ValueError): return False
    nodes = list(objects(data))
    spans = [n for n in nodes if 'name' in n and ('spanId' in n or 'span_id' in n)]
    names = {n['name'] for n in spans}
    service_names = {n.get('value', {}).get('stringValue', n.get('value', {}).get('string_value')) for n in nodes if n.get('key') == 'service.name'}
    if not expected <= names or not services <= service_names: return False
    # A shared trace ID alone is insufficient: publishing must descend directly
    # from the durable SQL-write context, including after asynchronous polling.
    persisted = {hex_id(s.get('spanId', s.get('span_id', ''))) for s in spans
                 if s['name'] in ('postgres.persist_decision', 'postgres.reference_transaction')}
    persisted.discard('')
    publishers = [s for s in spans if s['name'] == 'outbox.publish']
    if not any(hex_id(s.get('parentSpanId', s.get('parent_span_id', ''))) in persisted for s in publishers):
        return False
    for span in spans:
        require(hex_id(span.get('traceId', span.get('trace_id', ''))) == trace_id, 'Trace contains unrelated identity')
    return True


def main(mode):
    require(mode in ('healthy', 'collector-down'), 'Unknown mode')
    if mode == 'collector-down':
        try:
            with urllib.request.urlopen('http://otel-collector:13133/', timeout=2): pass
        except (OSError, urllib.error.URLError): pass
        else: raise RuntimeError('Collector is still reachable; outage not established')
    api.wait(lambda: api.healthy(api.BASES[0]), 'API startup')
    if mode == 'healthy':
        for dependency in ['http://otel-collector:13133/', os.environ.get('TEMPO_URL', 'http://tempo:3200') + '/ready']:
            def ready(url=dependency):
                try:
                    with urllib.request.urlopen(url, timeout=3) as response: return response.status == 200
                except OSError: return False
            api.wait(ready, 'Tracing dependency startup', seconds=120)
    internal, provider = api.access_token('wallet-service', os.environ['WALLET_CLIENT_SECRET']), api.access_token('provider-a', os.environ['PROVIDER_A_CLIENT_SECRET'])
    results = {'mode': mode, 'startedAt': datetime.now(timezone.utc).isoformat(), 'traces': []}

    player, wallet = api.opened(internal)
    body = api.operation(player, wallet)
    trace_id, header = parent()
    status, first = submit(provider, body, header)
    require(status == 200 and first['balance']['amount'] == '20.00', 'HTTP financial processing failed')
    _, other_header = parent()
    status, replay = submit(provider, body, other_header)
    require(status == 200 and replay['idempotentReplay'] and replay['transactionId'] == first['transactionId'],
            'Changing trace context changed idempotency')
    api.verify_wallet(internal, wallet, '20.00', 2)
    results['traces'].append({'path': 'HTTP', 'traceId': trace_id})
    if mode == 'healthy':
        api.wait(lambda: check_trace(trace_id, {'HTTP POST transactions', 'financial.transaction',
            'postgres.transaction', 'postgres.persist_decision', 'outbox.publish'}, {'wagering-api', 'wagering-workers'}),
            'HTTP/SQL/durable-outbox trace continuity', seconds=120)

    player, wallet = api.opened(internal)
    body = api.operation(player, wallet)
    trace_id, header = parent()
    envelope = {'messageId': api.identity(), 'type': 'WagerTransactionRequested',
                'occurredAt': datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z'),
                'data': dict(body, idempotencyKey='provider-a:' + body['externalTransactionId'])}
    credentials = json.loads(Path('/credentials/producer.json').read_text())
    producer = api.sqs_client(credentials['accessKeyId'], credentials['secretAccessKey'])
    producer.send_message(QueueUrl=os.environ['SQS_INPUT_QUEUE_URL'], MessageBody=json.dumps(envelope),
        MessageGroupId=wallet, MessageDeduplicationId=api.identity(),
        MessageAttributes={'traceparent': {'DataType': 'String', 'StringValue': header}})
    api.wait(lambda: (lookup(body, provider) or {}).get('status') == 'PROCESSED', 'SQS financial processing')
    api.verify_wallet(internal, wallet, '20.00', 2)
    results['traces'].append({'path': 'SQS', 'traceId': trace_id})
    if mode == 'healthy':
        api.wait(lambda: check_trace(trace_id, {'sqs.process', 'financial.inbox', 'postgres.inbox_transaction',
            'postgres.persist_decision', 'outbox.publish'}, {'wagering-consumer', 'wagering-workers'}),
            'SQS/SQL/durable-outbox trace continuity', seconds=120)

    player, wallet = api.opened(internal)
    bet = api.operation(player, wallet)
    refund = api.operation(player, wallet, 'REFUND', reference=bet['externalTransactionId'])
    trace_id, header = parent()
    status, pending = submit(provider, refund, header)
    require(status == 202 and pending['status'] == 'PENDING_REFERENCE', 'Reference did not enter durable wait')
    _, bet_header = parent()
    require(submit(provider, bet, bet_header)[0] == 200, 'Later reference failed')
    api.wait(lambda: (lookup(refund, provider) or {}).get('status') == 'PROCESSED', 'Reference recovery')
    api.verify_wallet(internal, wallet, '100.00', 3)
    results['traces'].append({'path': 'reference', 'traceId': trace_id})
    if mode == 'healthy':
        api.wait(lambda: check_trace(trace_id, {'reference.resume', 'postgres.reference_transaction',
            'outbox.publish'}, {'wagering-api', 'wagering-workers'}), 'Reference trace continuation', seconds=120)
    results['status'] = 'PASS'
    results['finishedAt'] = datetime.now(timezone.utc).isoformat()
    Path('/results/' + mode + '-' + str(time.time_ns()) + '.json').write_text(json.dumps(results, indent=2) + '\n')
    print(json.dumps(results, indent=2))
    print('PASS: ' + ('three real trace paths and financial invariants' if mode == 'healthy' else 'HTTP, SQS and reference processing while collector is unavailable'))


if __name__ == '__main__': main(sys.argv[1])
