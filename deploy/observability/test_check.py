"""Offline regression checks; no services, tokens or SDK imports are required."""
import ast
import contextlib
import copy
import io
import json
from pathlib import Path
import types
import unittest


class DashboardChecks(unittest.TestCase):
    def harness(self, level, conflict_status=409, apply_win=True):
        source = Path(__file__).with_name('check.py').read_text()
        tree = ast.parse(source)
        tree.body = [node for node in tree.body if isinstance(node, ast.FunctionDef)
                     and node.name in ('require', 'traffic')]
        calls = []
        state = {}
        def opened(_):
            state.update(balance=10000, version=1, seen={})
            return 'player', 'wallet'
        def operation(player, wallet, kind='BET', amount='80.00'):
            return {'kind': kind, 'money': {'amount': amount}, 'id': len(calls)}
        def submit(index, token, body):
            calls.append(copy.deepcopy(body))
            key = body['id']
            if key in state['seen']:
                previous, result = state['seen'][key]
                if previous != body:
                    return conflict_status, {'failureCode': 'IDEMPOTENCY_CONFLICT'}
                return 200, dict(result, idempotentReplay=True)
            amount = int(body['money']['amount'].replace('.', ''))
            if body['kind'] == 'BET' and amount > state['balance']:
                return 422, {'failureCode': 'INSUFFICIENT_FUNDS'}
            if body['kind'] == 'BET' or apply_win:
                state['balance'] += -amount if body['kind'] == 'BET' else amount
                state['version'] += 1
            result = {'status': 'PROCESSED', 'transactionId': str(key), 'idempotentReplay': False}
            state['seen'][key] = (copy.deepcopy(body), result)
            return 200, result
        def verify_wallet(_, wallet, amount, version):
            if state['balance'] != int(amount.replace('.', '')) or state['version'] != version:
                raise RuntimeError('Final balance/version mismatch')
        namespace = dict(copy=copy, json=json, os=types.SimpleNamespace(environ={
            'WALLET_CLIENT_SECRET': 'fixture', 'PROVIDER_A_CLIENT_SECRET': 'fixture'}),
            access_token=lambda *args: 'fixture', opened=opened, operation=operation,
            submit=submit, verify_wallet=verify_wallet, BASES=['fixture'],
            request=lambda *args: (200, {'balanceMinor': state['balance'], 'version': state['version']}))
        exec(compile(tree, 'check.py', 'exec', optimize=level), namespace)
        return namespace['traffic'], calls

    def test_operations_execute_at_every_optimization_level(self):
        for level in (0, 1, 2):
            with self.subTest(level=level):
                traffic, calls = self.harness(level)
                with contextlib.redirect_stdout(io.StringIO()):
                    traffic()
                self.assertEqual(len(calls), 15)
                self.assertEqual(sum(c['kind'] == 'WIN' for c in calls), 3)

    def test_missing_identity_conflict_fails_at_every_level(self):
        for level in (0, 1, 2):
            with self.subTest(level=level):
                traffic, _ = self.harness(level, conflict_status=200)
                with self.assertRaisesRegex(RuntimeError, 'identity conflict'):
                    traffic()

    def test_missing_win_effect_fails_at_every_level(self):
        for level in (0, 1, 2):
            with self.subTest(level=level):
                traffic, _ = self.harness(level, apply_win=False)
                with contextlib.redirect_stdout(io.StringIO()):
                    with self.assertRaisesRegex(RuntimeError, 'balance/version'):
                        traffic()

    def test_no_removable_assertions(self):
        tree = ast.parse(Path(__file__).with_name('check.py').read_text())
        self.assertFalse(any(isinstance(node, ast.Assert) for node in ast.walk(tree)))


if __name__ == '__main__':
    unittest.main()
