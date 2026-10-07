"""SQS benchmark observations, using the same bounded API metric probes as HTTP."""
import json
import math
import re
import sys
import time
import urllib.request
from pathlib import Path
import observe

ROOT = Path('/results')
original_metrics = observe.metrics


def metrics(base):
    row = original_metrics(base)
    with urllib.request.urlopen(base + '/metrics', timeout=5) as response:
        lines = response.read().decode().splitlines()
    depths = {}
    for line in lines:
        if line.startswith('wagering_sqs_messages{'):
            queue = re.search(r'queue="([^"]+)"', line)
            state = re.search(r'state="([^"]+)"', line)
            if queue and state and queue[1] in ('input', 'input_dlq'):
                value = float(line.rsplit(' ', 1)[1])
                if not math.isfinite(value): raise RuntimeError('Queue depth is unavailable')
                depths[queue[1] + '/' + state[1]] = value
    if len(depths) != 6: raise RuntimeError('Expected six finite input/DLQ depth series')
    row['inputDepth'] = sum(v for k, v in depths.items() if k.startswith('input/'))
    row['inputDLQDepth'] = sum(v for k, v in depths.items() if k.startswith('input_dlq/'))
    return row


observe.metrics = metrics


def queues_empty(label):
    start = time.time()
    while time.time() - start < 180:
        row = metrics(observe.APIS[0])
        if row['inputDLQDepth'] != 0: raise RuntimeError('Input DLQ is nonempty; inspect it, do not purge')
        if row['sampleTimestamp'] >= start and row['inputDepth'] == 0:
            (ROOT / (label + '-queue-drain.json')).write_text(json.dumps({'seconds': time.time()-start,
                'method': 'fresh approximate input depth observation; separate SQL inbox audit required'}))
            return
        time.sleep(1)
    raise RuntimeError('Input queue did not become observably empty')


def paused():
    start = time.time()
    while time.time() - start < 30:
        row = metrics(observe.APIS[0])
        if row['sampleTimestamp'] >= start and row['outbox_pending'] > 0:
            (ROOT / 'paused-backlog.json').write_text(json.dumps(row))
            print('PASS: unpublished outbox backlog observed with publishers paused', flush=True)
            return
        time.sleep(1)
    raise RuntimeError('No fresh positive backlog while publishers paused')


def report():
    samples = [json.loads(s) for s in (ROOT/'metrics.jsonl').read_text().splitlines() if s.strip()]
    paused_row = json.loads((ROOT/'paused-backlog.json').read_text())
    if paused_row['outbox_pending'] <= 0: raise RuntimeError('Missing pause evidence')
    lines = ['# Measured SQS performance', '',
        '| Case | Offered msg/s | Accepted/configured s | Envelopes | Unique movements | Generator drops | Send p50/p95/p99 ms | Observed terminal p50/p95/p99 ms | Terminal drain s | Peak sampled input | Peak sampled outbox | Peak sampled outbox age s | Outbox drain observed s |',
        '| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |']
    notes = []
    for case in ('many','duplicates','outage'):
        doc = json.loads((ROOT/(case+'-measure.json')).read_text())
        if doc['status'] != 'PASS' or doc['sendErrors'] or doc['pollErrors']:
            raise RuntimeError('Generator did not pass: ' + case)
        if not doc['envelopes'] or not doc['operations']: raise RuntimeError('Empty workload')
        if any(o['observedTerminalMs'] <= 0 or o.get('error') for o in doc['operations']):
            raise RuntimeError('Missing observed terminal result')
        if doc['scheduledSlots'] != len(doc['envelopes']) + doc['generatorDropped'] + doc['sendErrors']:
            raise RuntimeError('Scheduled slot accounting mismatch')
        if case != 'duplicates' and len(doc['envelopes']) != len(doc['operations']):
            raise RuntimeError('Unique workload count mismatch')
        if case == 'duplicates' and len(doc['envelopes']) <= len(doc['operations']):
            raise RuntimeError('No application duplicates exercised')
        phase = [s for s in samples if s['stage'] == case+'-measure']
        if len(phase)<2 or any('error' in s for s in phase): raise RuntimeError('Incomplete samples: ' + case)
        if any(s['apis'][0]['inputDLQDepth'] != 0 for s in phase): raise RuntimeError('Observed DLQ messages')
        def quantiles(name):
            q=doc[name]
            if any(not math.isfinite(q[k]) or q[k]<=0 for k in ('p50','p95','p99')):
                raise RuntimeError('Missing positive percentiles')
            return '/'.join(f'{q[k]:.3f}' for k in ('p50','p95','p99'))
        drain = json.loads((ROOT/(case+'-drain.json')).read_text())['seconds']
        row = [case,doc['offeredMessagesPerSecond'],round(doc['acceptedPerConfiguredSecond'],3),
            len(doc['envelopes']),len(doc['operations']),doc['generatorDropped'],quantiles('sendLatencyMs'),
            quantiles('observedTerminalLatencyMs'),round(doc['observedTerminalDrainSeconds'],3),
            max(s['apis'][0]['inputDepth'] for s in phase),max(s['apis'][0]['outbox_pending'] for s in phase),
            max(s['apis'][0]['outbox_oldest_seconds'] for s in phase),round(drain,3)]
        lines.append('| '+' | '.join(map(str,row))+' |')
        conflicts = sum(a['databaseConflicts'] for a in phase[-1]['apis'])-sum(a['databaseConflicts'] for a in phase[0]['apis'])
        if conflicts < 0: raise RuntimeError('Metric counter reset')
        notes.append(f'- {case}: {len(phase)} samples; surfaced API database-conflict delta {conflicts}. Consumer conflicts are not exposed by this API counter.')
    lines += ['', *notes, '',
        'Send latency measures one SDK SendMessage call. Observed terminal latency measures from the first successful send attempt start for a business identity to its first PROCESSED HTTP observation.',
        'Terminal latency includes polling and observer scheduling delay; it is an upper bound on commit latency. It excludes duplicate-envelope completion latency, which is verified separately by SQL.',
        'All envelopes use unique transport deduplication IDs and unique inbox message IDs. The duplicates case reuses business identity in adjacent pairs.',
        'The serial producer offers fixed slots, skips missed slots and never bursts to catch up. Throughput divides accepted envelopes by configured duration.',
        'Observer concurrency is 16 with 250 ms between rounds. Its HTTP requests share API and database resources with processing.',
        'Terminal drain starts at the last send return. Queue drain uses fresh approximate depths; SQL independently requires all accepted envelopes in the inbox.',
        'All monetary operations are WIN 0.01 BRL. Unique movement counts are compared against exact expected balances, versions and reconciled entries through every API.',
        'Only the outage case pauses both publishers. Consumers and APIs remain active. Outbox drain is sampled after publishers restart; controller startup and probe resolution affect the measurement.',
        'Two normal Go consumers, three APIs and two publishers; tracing disabled. No race instrumentation in measured binaries; producer unit tests run with -race during image build.',
        'Warmup results are separate; many/duplicates use 30 s warmup + 120 s measurement. The outage case uses one 20 s measured window without warmup.',
        'One run is not a capacity estimate or a security certification; the documented MiniStack signature limitation remains.']
    (ROOT/'SQS-PERFORMANCE.md').write_text('\n'.join(lines)+'\n')
    print('PASS: measured SQS report generated', flush=True)


if __name__ == '__main__':
    command=sys.argv[1]
    if command=='sample': observe.sample()
    elif command=='ready': observe.ready(); queues_empty('startup')
    elif command=='queues': queues_empty(sys.argv[2])
    elif command=='drain': observe.drain(sys.argv[2])
    elif command=='paused': paused()
    elif command=='report': report()
    else: raise ValueError('Unknown command')
