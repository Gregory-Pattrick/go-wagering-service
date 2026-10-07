import asyncio
from datetime import datetime, timedelta, timezone
import unittest

from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials
from signature_gate import Denied, SignatureGate, verify


def signed(secret='correct-secret', service='sqs', region='us-east-1', stamp=None):
    body = b'{"QueueName":"signature-test.fifo"}' if service == 'sqs' else b'Action=ListUsers&Version=2010-05-08'
    headers = {'Content-Type': 'application/x-amz-json-1.0' if service == 'sqs' else 'application/x-www-form-urlencoded'}
    if service == 'sqs':
        headers['X-Amz-Target'] = 'AmazonSQS.GetQueueUrl'
    request = AWSRequest(method='POST', url='http://ministack:4566/', data=body, headers=headers)
    signer = SigV4Auth(Credentials('VALIDKEY', secret), service, region)
    if stamp:
        request.context['timestamp'] = stamp
        request.headers['X-Amz-Date'] = stamp
        canonical = signer.canonical_request(request)
        signature = signer.signature(signer.string_to_sign(request, canonical), request)
        signer._inject_signature_to_request(request, signature)
    else:
        signer.add_auth(request)
    scope = {'type': 'http', 'method': 'POST', 'path': '/', 'raw_path': b'/', 'query_string': b'',
             'headers': [(k.lower().encode(), str(v).encode()) for k, v in request.headers.items()]}
    # The HTTP transport adds Host after signing; it is part of the signature.
    scope['headers'].append((b'host', b'ministack:4566'))
    return scope, body


def secret(key):
    if key != 'VALIDKEY':
        raise Denied('unknown')
    return 'correct-secret'


class SignatureTests(unittest.TestCase):
    def test_sdk_sqs_and_iam_signatures(self):
        for service in ('sqs', 'iam'):
            scope, body = signed(service=service)
            verify(scope, body, secret)

    def test_wrong_secret(self):
        scope, body = signed(secret='wrong')
        with self.assertRaises(Denied):
            verify(scope, body, secret)

    def test_body_and_target_tampering(self):
        scope, body = signed()
        with self.assertRaises(Denied):
            verify(scope, body + b' ', secret)
        scope['headers'] = [(k, b'AmazonSQS.DeleteQueue' if k == b'x-amz-target' else v) for k, v in scope['headers']]
        with self.assertRaises(Denied):
            verify(scope, body, secret)

    def test_expired_future_and_wrong_region(self):
        now = datetime.now(timezone.utc)
        for delta in (-600, 600):
            scope, body = signed(stamp=(now + timedelta(seconds=delta)).strftime('%Y%m%dT%H%M%SZ'))
            with self.assertRaises(Denied):
                verify(scope, body, secret)
        scope, body = signed(region='eu-west-1')
        with self.assertRaises(Denied):
            verify(scope, body, secret)

    def test_unsigned_duplicate_headers_and_unknown_key(self):
        scope, body = signed()
        scope['headers'].append((b'host', b'ministack:4566'))
        with self.assertRaises(Denied):
            verify(scope, body, secret)
        scope, body = signed()
        scope['headers'] = [(k, v) for k, v in scope['headers'] if k != b'authorization']
        with self.assertRaises(Denied):
            verify(scope, body, secret)
        scope, body = signed()
        scope['headers'] = [(k, v.replace(b'VALIDKEY/', b'OTHERKEY/')) for k, v in scope['headers']]
        with self.assertRaises(Denied):
            verify(scope, body, secret)

    def test_admin_route_query_and_host_bypass(self):
        for change in ({'path': '/_ministack/reset'}, {'query_string': b'Action=DeleteQueue'}, {'raw_path': b'/%2e'}):
            scope, body = signed()
            scope.update(change)
            with self.assertRaises(Denied):
                verify(scope, body, secret)
        scope, body = signed()
        scope['headers'] = [(k, b'bucket.s3.localhost' if k == b'host' else v) for k, v in scope['headers']]
        with self.assertRaises(Denied):
            verify(scope, body, secret)

    def test_unsigned_payload_marker(self):
        scope, body = signed()
        scope['headers'].append((b'x-amz-content-sha256', b'UNSIGNED-PAYLOAD'))
        with self.assertRaises(Denied):
            verify(scope, body, secret)

    def test_asgi_never_dispatches_invalid_signature(self):
        async def run(valid):
            scope, body = signed(secret='correct-secret' if valid else 'wrong')
            dispatched, output = [], []
            async def downstream(s, receive, send):
                dispatched.append(await receive())
            async def receive():
                return {'type': 'http.request', 'body': body}
            async def send(event):
                output.append(event)
            await SignatureGate(downstream, secret)(scope, receive, send)
            if valid:
                self.assertEqual(dispatched[0]['body'], body)
            else:
                self.assertEqual(dispatched, [])
                self.assertEqual(output[0]['status'], 403)
        asyncio.run(run(False))
        asyncio.run(run(True))

    def test_resolver_failure_is_not_forwarded(self):
        async def run():
            scope, body = signed()
            output = []
            async def downstream(*args):
                self.fail('must not forward')
            async def receive():
                return {'type': 'http.request', 'body': body}
            async def send(event):
                output.append(event)
            def broken(key):
                raise RuntimeError('unavailable')
            await SignatureGate(downstream, broken)(scope, receive, send)
            self.assertEqual(output[0]['status'], 503)
        asyncio.run(run())


if __name__ == '__main__':
    unittest.main()
