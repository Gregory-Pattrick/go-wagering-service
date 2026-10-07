"""Sample existing metrics and render only measured HTTP benchmark results."""
import json
import math
import re
import sys
import time
import urllib.request
from pathlib import Path

ROOT = Path('/results')
APIS = ['http://api-1:9090', 'http://api-2:9090', 'http://api-3:9090']


def metrics(base):
    with urllib.request.urlopen(base + '/metrics', timeout=5) as response:
        lines = response.read().decode().splitlines()
    result = {'databaseConflicts': 0, 'identityConflicts': 0}
    for line in lines:
        if line.startswith('wagering_database_state{'):
            match = re.search(r'measure="([^"]+)"', line)
            if match and match[1] in ('outbox_pending', 'outbox_oldest_seconds'):
                value = float(line.rsplit(' ', 1)[1])
                if not math.isfinite(value):
                    raise RuntimeError('Database metrics are unavailable')
                result[match[1]] = value
        elif line.startswith('wagering_dependency_sample_timestamp_seconds '):
            result['sampleTimestamp'] = float(line.rsplit(' ', 1)[1])
        elif line.startswith('wagering_operation_errors_total{'):
            value = float(line.rsplit(' ', 1)[1])
            if 'category="database_conflict"' in line:
                result['databaseConflicts'] += value
            if 'category="identity_conflict"' in line:
                result['identityConflicts'] += value
    if 'outbox_pending' not in result or 'sampleTimestamp' not in result:
        raise RuntimeError('Missing database metrics')
    if time.time() - result['sampleTimestamp'] > 15:
        raise RuntimeError('Stale database metrics')
    return result


def sample():
    with (ROOT / 'metrics.jsonl').open('a', buffering=1) as output:
        while True:
            row = {'time': time.time(), 'stage': (ROOT / 'stage.txt').read_text().strip()
                   if (ROOT / 'stage.txt').exists() else 'startup'}
            try:
                row['apis'] = [metrics(base) for base in APIS]
            except Exception as error:
                row['error'] = str(error)
            output.write(json.dumps(row) + '\n')
            time.sleep(1)


def ready():
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        try:
            for base in APIS:
                with urllib.request.urlopen(base + '/health/ready', timeout=5) as response:
                    if response.status != 200:
                        raise RuntimeError('Not ready')
                metrics(base)
            print('PASS: three APIs ready with fresh metrics', flush=True)
            return
        except (OSError, ValueError, RuntimeError):
            time.sleep(1)
    raise RuntimeError('API readiness timed out')


def drain(label):
    start = time.time()
    while time.time() - start < 180:
        row = metrics(APIS[0])
        if row['sampleTimestamp'] >= start and row['outbox_pending'] == 0:
            (ROOT / (label + '-drain.json')).write_text(json.dumps({
                'seconds': time.time() - start, 'observedAt': time.time(),
                'method': 'polling fresh five-second database probe; SQL audit follows'}))
            print('PASS: observed outbox drain for ' + label, flush=True)
            return
        time.sleep(1)
    raise RuntimeError('Outbox drain timed out')


def render():
    samples = [json.loads(s) for s in (ROOT / 'metrics.jsonl').read_text().splitlines() if s.strip()]
    lines = ['# Measured HTTP performance', '',
             'Generated from this run; see environment.json and raw k6 JSON files.', '',
             '| Case | Offered iterations/s | Completed requests / configured second | p50 ms | p95 ms | p99 ms | Unexpected rate | Expected rejections | Replays | Unique movements | Dropped iterations | Peak sampled outbox | Peak sampled age s | Drain observed s |',
             '| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |']
    details = []
    for case in ('many', 'hot', 'replay'):
        document = json.loads((ROOT / (case + '-measure.json')).read_text())
        m = document['summary']['metrics']
        def value(name, field, default=0):
            return m.get(name, {}).get('values', {}).get(field, default)
        if value('completed_audits', 'count') != 1 or value('unexpected_response', 'rate', 1) != 0:
            raise RuntimeError('Invalid financial audit or unexpected response in ' + case)
        for field in ('med', 'p(95)', 'p(99)'):
            latency = value('transaction_latency_ms', field, None)
            if latency is None or not math.isfinite(latency):
                raise RuntimeError('Missing measured latency: ' + case + '/' + field)
        if value('transaction_attempts', 'count') <= 0:
            raise RuntimeError('No measured requests: ' + case)
        unique = value('unique_movements', 'count')
        if unique != (1 if case == 'replay' else value('processed_responses', 'count')):
            raise RuntimeError('HTTP outcomes disagree with unique committed movements: ' + case)
        for name, metric in m.items():
            if any(not t.get('ok', False) for t in metric.get('thresholds', {}).values()):
                raise RuntimeError('Failed k6 threshold: ' + name)
        phase = [s for s in samples if s['stage'] == case + '-measure']
        good = [s for s in phase if 'apis' in s]
        if len(good) < 2 or any('error' in s for s in phase):
            raise RuntimeError('Incomplete metric samples for ' + case)
        peak = max(s['apis'][0]['outbox_pending'] for s in good)
        age = max(s['apis'][0]['outbox_oldest_seconds'] for s in good)
        observed_drain = json.loads((ROOT / (case + '-drain.json')).read_text())['seconds']
        row = [case, document['offeredIterationsPerSecond'],
               round(value('transaction_attempts', 'count') / document['durationSeconds'], 3),
               value('transaction_latency_ms', 'med'), value('transaction_latency_ms', 'p(95)'),
               value('transaction_latency_ms', 'p(99)'), value('unexpected_response', 'rate'),
               value('expected_rejections', 'count'), value('replay_responses', 'count'), unique,
               value('dropped_iterations', 'count'), peak, age, round(observed_drain, 3)]
        lines.append('| ' + ' | '.join(str(x) for x in row) + ' |')
        delta = sum(a['databaseConflicts'] for a in good[-1]['apis']) - sum(a['databaseConflicts'] for a in good[0]['apis'])
        if delta < 0:
            raise RuntimeError('Counter reset during measurement')
        details.append(f'- {case}: {len(good)} metric samples; surfaced database-conflict counter delta {delta}.')
    lines += ['', *details, '',
              'Latency covers the transaction HTTP request (including expected 422 responses), excluding token acquisition and setup/audit calls.',
              'Throughput divides completed transaction attempts by configured arrival duration; late completions during the 30-second grace period remain counted.',
              'Each iteration sends one measured transaction request. Dropped iterations are reported, not hidden or counted as server failures.',
              'Outbox values are shared database snapshots from API 1 only. Peak values are sampled bounds, not guaranteed instantaneous maxima.',
              'Conflict deltas span the first and last phase samples and only include surfaced errors, not internal SQL retries or pure lock wait.',
              'Drain timing starts after k6 teardown and is limited by probe/poll resolution; it is not per-event publication latency.',
              'Warmups use separate wallets and are excluded from this table. Replay setup creates one original transaction; measured requests must all be replays.',
              'Normal Go build, tracing disabled, metrics enabled, three APIs and two publishers; generator and services share Docker host resources.',
              'SQS load, async end-to-end latency and controlled publication-outage load recovery are not measured by this HTTP block.',
              'No minimum RPS was specified by the challenge. This run measures the configured workload, not maximum system capacity.']
    (ROOT / 'PERFORMANCE.md').write_text('\n'.join(lines) + '\n')
    print('PASS: measured report generated', flush=True)


if __name__ == '__main__':
    command = sys.argv[1]
    if command == 'sample': sample()
    elif command == 'ready': ready()
    elif command == 'drain': drain(sys.argv[2])
    elif command == 'report': render()
    else: raise ValueError('Unknown command')
