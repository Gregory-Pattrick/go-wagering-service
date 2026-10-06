"""Exercise real financial traffic and query the provisioned dashboard data."""
import base64
import copy
import json
import math
import os
from pathlib import Path
import sys
import time
import urllib.parse
import urllib.request

sys.path.insert(0, '/distributed')
from run import access_token, opened, operation, submit, verify_wallet, wait, request, BASES


def get(url, auth=False):
    req = urllib.request.Request(url)
    if auth:
        credential = ('admin:' + os.environ['GRAFANA_ADMIN_PASSWORD']).encode()
        req.add_header('Authorization', 'Basic ' + base64.b64encode(credential).decode())
    with urllib.request.urlopen(req, timeout=10) as response:
        return json.load(response)


def query(expression):
    result = get('http://prometheus:9090/api/v1/query?' + urllib.parse.urlencode({'query': expression}))
    assert result['status'] == 'success', result
    return result['data']['result']


def values(expression):
    return [float(item['value'][1]) for item in query(expression)]


def observed(expression, predicate):
    try:
        found = values(expression)
    except (OSError, ValueError):
        return False
    return bool(found) and all(math.isfinite(x) and predicate(x) for x in found)


def traffic():
    internal = access_token('wallet-service', os.environ['WALLET_CLIENT_SECRET'])
    provider = access_token('provider-a', os.environ['PROVIDER_A_CLIENT_SECRET'])

    for _ in range(3):
        player, wallet = opened(internal)
        bet = operation(player, wallet)

        code, first = submit(0, provider, bet)
        assert code == 200 and first['status'] == 'PROCESSED'

        code, replay = submit(0, provider, bet)
        assert code == 200 and replay['idempotentReplay']
        assert replay['transactionId'] == first['transactionId']

        changed = copy.deepcopy(bet)
        changed['money']['amount'] = '79.00'
        assert submit(0, provider, changed)[0] == 409

        rejected_code, rejected_result = submit(
            0, provider, operation(player, wallet)
        )
        assert rejected_code == 422

        win = operation(player, wallet, 'WIN', '30.00')
        win_code, win_result = submit(0, provider, win)
        assert win_code == 200

        wallet_status, wallet_result = request(
            BASES[0], 'GET', '/wallets/' + wallet, internal
        )

        print(json.dumps({
            "walletId": wallet,
            "httpStatus": wallet_status,
            "expectedBalance": "50.00",
            "expectedVersion": 3,
            "actual": wallet_result,
            "betRequest": bet,
            "betResponse": first,
            "rejectionResponse": rejected_result,
            "winRequest": win,
            "winResponse": win_result,
        }, indent=2), flush=True)

        verify_wallet(internal, wallet, '50.00', 3)

    print(
        'PASS: real BET/WIN, replay, conflict, rejection and reconciliation',
        flush=True,
    )




def dashboard():
    expected = json.loads(Path('/observability/dashboards/wagering.json').read_text())
    def loaded():
        try:
            actual = get('http://grafana:3000/api/dashboards/uid/wagering-operations', True)
            return actual['dashboard']['title'] == expected['title']
        except (OSError, ValueError, KeyError):
            return False
    wait(loaded, 'Grafana dashboard provisioning', seconds=90)
    datasource = get('http://grafana:3000/api/datasources/uid/wagering-prometheus', True)
    assert datasource['url'] == 'http://prometheus:9090'
    # Use the Grafana datasource proxy too: direct Prometheus reachability is not enough.
    proxy = get('http://grafana:3000/api/datasources/proxy/uid/wagering-prometheus/api/v1/query?' +
                urllib.parse.urlencode({'query': 'up{job=~"wagering-.*"}'}), True)
    assert proxy['status'] == 'success' and len(proxy['data']['result']) == 3
    for panel in expected['panels']:
        for target in panel['targets']:
            result = query(target['expr'])
            finite = sum(math.isfinite(float(item['value'][1])) for item in result)
            print(f"QUERY OK: {panel['title']} ({finite} finite series)", flush=True)
    # Rare retries and conflicts may have no series; never convert missing data to zero.
    for expression in [
        'wagering_http_duration_seconds_count{job="wagering-api",route="transactions"}',
        'wagering_financial_outcomes_total{transport="http",replay="true"}',
        'wagering_database_state{job="wagering-api",measure="transactions_rejected"}',
    ]:
        assert observed(expression, lambda x: x > 0), expression
    assert observed('wagering_sqs_messages{job="wagering-api"}', lambda x: x >= 0)
    print('PASS: Grafana provisioning, datasource proxy and dashboard PromQL', flush=True)


def main():
    phase = sys.argv[1]
    backlog = 'wagering_database_state{job="wagering-api",measure="outbox_pending"}'
    if phase == 'traffic':
        wait(lambda: observed('up{job="wagering-api"}', lambda x: x == 1), 'API scrape')
        traffic()
        wait(lambda: observed(backlog, lambda x: x > 0), 'observable outbox backlog')
        wait(lambda: observed('wagering_database_state{job="wagering-api",measure="outbox_oldest_seconds"}', lambda x: x > 0), 'outbox age')
        print('PASS: paused publisher produced measurable backlog and age', flush=True)
    elif phase == 'verify':
        wait(lambda: len(values('up{job=~"wagering-.*"}')) == 3 and
             observed('up{job=~"wagering-.*"}', lambda x: x == 1), 'all three scrape targets')
        wait(lambda: observed(backlog, lambda x: x == 0), 'outbox drain', seconds=120)
        # Give rate histograms enough samples after startup or restart.
        time.sleep(12)
        dashboard()
        print('PASS: publisher recovered and outbox drained', flush=True)
    else:
        raise ValueError('Expected traffic or verify')


if __name__ == '__main__':
    main()
