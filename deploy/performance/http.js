import http from 'k6/http';
import exec from 'k6/execution';
import crypto from 'k6/crypto';
import { Counter, Rate, Trend } from 'k6/metrics';

const bases = ['http://api-1:8080', 'http://api-2:8080', 'http://api-3:8080'];
const mode = __ENV.PERF_CASE || 'many';
const phase = __ENV.PERF_PHASE || 'measure';
const rate = Number(__ENV.PERF_RATE || 20);
const seconds = Number(__ENV.PERF_DURATION || 120);
if (!['many', 'hot', 'replay'].includes(mode) || !['warmup', 'measure'].includes(phase) ||
    !Number.isInteger(rate) || rate < 1 || rate > 200 ||
    !Number.isInteger(seconds) || seconds < 10 || seconds > 180) {
  throw new Error('Invalid benchmark case, phase, rate (1..200) or duration (10..180 seconds)');
}
const latency = new Trend('transaction_latency_ms', true);
const attempts = new Counter('transaction_attempts');
const processed = new Counter('processed_responses');
const replays = new Counter('replay_responses');
const rejected = new Counter('expected_rejections');
const failures = new Rate('unexpected_response');
const reconciled = new Counter('reconciled_wallets');
const completed = new Counter('completed_audits');
const movements = new Counter('unique_movements');
export const options = {
  scenarios: { load: { executor: 'constant-arrival-rate', rate, timeUnit: '1s',
    duration: `${seconds}s`, preAllocatedVUs: 10, maxVUs: 50, gracefulStop: '30s' } },
  setupTimeout: '180s', teardownTimeout: '180s',
  summaryTrendStats: ['min', 'avg', 'med', 'p(95)', 'p(99)', 'max'],
  systemTags: ['status', 'method', 'name', 'scenario', 'expected_response'],
  thresholds: { transaction_attempts: ['count>0'], unexpected_response: ['rate==0'], completed_audits: ['count==1'],
    reconciled_wallets: [`count==${mode === 'many' ? 50 : 1}`] },
};
function must(ok, message) { if (!ok) throw new Error(message); }
function uuid() {
  const b = new Uint8Array(crypto.randomBytes(16));
  b[6] = (b[6] & 15) | 64; b[8] = (b[8] & 63) | 128;
  const h = Array.from(b, x => x.toString(16).padStart(2, '0')).join('');
  return `${h.slice(0,8)}-${h.slice(8,12)}-${h.slice(12,16)}-${h.slice(16,20)}-${h.slice(20)}`;
}
function token(client, secret) {
  const r = http.post('http://keycloak:8080/realms/wagering/protocol/openid-connect/token',
    { grant_type: 'client_credentials', client_id: client, client_secret: secret },
    { timeout: '10s', tags: { name: 'token' } });
  if (r.status !== 200) { failures.add(true); throw new Error('Token request failed: HTTP ' + r.status); }
  return r.json().access_token;
}
function params(access, name, key) {
  const headers = { Authorization: 'Bearer ' + access, 'Content-Type': 'application/json' };
  if (key) headers['Idempotency-Key'] = key;
  return { headers, timeout: '15s', tags: { name } };
}
function body(wallet, id) {
  return { providerId: 'provider-a', externalTransactionId: id, playerId: wallet.playerId,
    walletId: wallet.id, roundId: 'performance', gameId: 'performance',
    kind: mode === 'many' ? 'WIN' : 'BET',
    money: { amount: mode === 'many' ? '0.01' : '1.00', currency: 'BRL' } };
}
export function setup() {
  const access = token('wallet-service', __ENV.WALLET_CLIENT_SECRET);
  const wallets = [];
  for (let i = 0; i < (mode === 'many' ? 50 : 1); i++) {
    const playerId = uuid();
    const r = http.post(bases[i % 3] + '/wallets', JSON.stringify({ playerId,
      initialBalance: { amount: '100.00', currency: 'BRL' } }), params(access, 'open_wallet'));
    must(r.status === 201, 'Wallet setup failed: HTTP ' + r.status);
    wallets.push({ id: r.json().id, playerId });
  }
  const duplicate = uuid();
  let original = null;
  if (mode === 'replay') {
    const access = token('provider-a', __ENV.PROVIDER_A_CLIENT_SECRET);
    const r = http.post(bases[0] + '/wagering/transactions', JSON.stringify(body(wallets[0], duplicate)),
      params(access, 'seed_replay', 'provider-a:' + duplicate));
    must(r.status === 200 && r.json().status === 'PROCESSED', 'Replay seed failed');
    original = r.json().transactionId;
  }
  return { wallets, duplicate, original };
}
let providerToken = null;
let tokenAt = 0;
export default function(data) {
  if (!providerToken || Date.now() - tokenAt > 60000) {
    providerToken = token('provider-a', __ENV.PROVIDER_A_CLIENT_SECRET); tokenAt = Date.now();
  }
  const iteration = exec.scenario.iterationInTest;
  const wallet = data.wallets[iteration % data.wallets.length];
  const id = mode === 'replay' ? data.duplicate : uuid();
  const r = http.post(bases[iteration % 3] + '/wagering/transactions', JSON.stringify(body(wallet, id)),
    params(providerToken, 'wager_transaction', 'provider-a:' + id));
  attempts.add(1); latency.add(r.timings.duration);
  let result = {}; try { result = r.json(); } catch (_) { /* Count invalid bodies as unexpected. */ }
  const ok = r.status === 200 && result.status === 'PROCESSED';
  const rejection = mode === 'hot' && r.status === 422 && result.failureCode === 'INSUFFICIENT_FUNDS';
  const validReplay = mode !== 'replay' || (result.idempotentReplay === true && result.transactionId === data.original && result.balance && result.balance.amount === '99.00');
  failures.add(!(ok && validReplay) && !rejection);
  if (ok) processed.add(1);
  if (ok && result.idempotentReplay) replays.add(1);
  if (rejection) rejected.add(1);
}
function minor(decimal) {
  must(typeof decimal === 'string' && /^\d+\.\d{2}$/.test(decimal), 'Invalid money representation');
  const value = Number(decimal.replace('.', ''));
  must(Number.isSafeInteger(value), 'Money exceeds safe verification range'); return value;
}
export function teardown(data) {
  const access = token('wallet-service', __ENV.WALLET_CLIENT_SECRET);
  for (const wallet of data.wallets) {
    let walletVersion = null;
    for (const base of bases) {
      const r = http.get(base + '/wallets/' + wallet.id, params(access, 'verify_wallet'));
      must(r.status === 200, 'Wallet read failed');
      const w = r.json(); const balance = minor(w.balance.amount);
      if (walletVersion !== null) must(w.version === walletVersion, 'API reads disagree on wallet version');
      walletVersion = w.version;
      const expected = mode === 'many' ? 10000 + w.version - 1 :
        mode === 'hot' ? 10000 - 100 * (w.version - 1) : 9900;
      must(balance === expected && balance >= 0, 'Post-load wallet invariant failed');
      if (mode === 'replay') must(w.version === 2, 'Replay moved money more than once');
      const c = http.post(base + '/wallets/' + wallet.id + '/reconciliation', null, params(access, 'reconciliation'));
      must(c.status === 200, 'Reconciliation request failed');
      const check = c.json();
      must(check.consistent && check.difference.amount === '0.00' && check.checkedEntries === w.version,
        'Post-load ledger reconciliation failed');
    }
    movements.add(walletVersion - 1);
    reconciled.add(1);
  }
  completed.add(1);
}
export function handleSummary(data) {
  return { [`/results/${mode}-${phase}.json`]: JSON.stringify({ case: mode, phase,
    offeredIterationsPerSecond: rate, durationSeconds: seconds, summary: data }, null, 2),
    stdout: `Saved ${mode}-${phase}.json; check process exit code and audit gates.\n` };
}
