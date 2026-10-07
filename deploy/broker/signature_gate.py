"""Narrow local SQS/IAM SigV4 gate. Not a general AWS authentication server."""
import asyncio
from datetime import datetime, timezone
import hashlib
import hmac
import json
import os
import re

from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials


class Denied(ValueError):
    pass


def resolve_secret(key):
    # This adapter is deliberately pinned to the inspected MiniStack version.
    # Use its current IAM state, not a separate credential file/cache.
    from ministack.core.iam_evaluator import (
        CredentialResolutionError, is_root_access_key, resolve_credential,
    )
    configured = os.environ.get('AWS_ACCESS_KEY_ID', 'test')
    if is_root_access_key(key) and key != configured:
        raise Denied('Unsupported root alias')
    credential = resolve_credential(
        key, os.environ.get('MINISTACK_ACCOUNT_ID', '000000000000'), session_token='')
    if isinstance(credential, CredentialResolutionError):
        raise Denied('Unknown or inactive credentials')
    return credential.secret_access_key


def verify(scope, body, resolver=resolve_secret, now=None):
    if scope['method'] != 'POST' or scope.get('query_string', b''):
        raise Denied('Only header-signed POST requests are supported')
    path = scope['path']
    if not re.fullmatch(r'/(?:[0-9]{12}/[A-Za-z0-9_-]+\.fifo)?', path):
        raise Denied('Unsupported path')
    if scope.get('raw_path', path.encode()) != path.encode():
        raise Denied('Encoded path is unsupported')
    headers = {}
    for raw_name, raw_value in scope.get('headers', []):
        name = raw_name.decode('ascii').lower()
        value = raw_value.decode('ascii')
        if name in headers or '\r' in value or '\n' in value:
            raise Denied('Ambiguous headers')
        headers[name] = value
    if 'content-encoding' in headers or 'x-amz-security-token' in headers:
        raise Denied('Compression and temporary credentials are unsupported')
    auth = re.fullmatch(
        r'AWS4-HMAC-SHA256 Credential=([A-Za-z0-9]+)/([0-9]{8})/([a-z0-9-]+)/(sqs|iam)/aws4_request, *'
        r'SignedHeaders=([a-z0-9;-]+), *Signature=([0-9a-f]{64})',
        headers.get('authorization', ''))
    if auth is None:
        raise Denied('Missing or malformed authorization')
    key, date, region, service, signed, supplied = auth.groups()
    if region != os.environ.get('MINISTACK_REGION', 'us-east-1'):
        raise Denied('Wrong signing region')
    names = signed.split(';')
    if names != sorted(set(names)) or any(n not in headers for n in names):
        raise Denied('Invalid signed headers')
    required = {'host', 'x-amz-date', 'content-type'}
    target = headers.get('x-amz-target', '')
    if service == 'sqs':
        if not re.fullmatch(r'AmazonSQS\.[A-Za-z]+', target):
            raise Denied('Unsupported SQS protocol')
        required.add('x-amz-target')
    elif target or path != '/' or not headers.get('content-type', '').startswith('application/x-www-form-urlencoded'):
        raise Denied('Unsupported IAM protocol')
    if not required.issubset(names):
        raise Denied('Unsigned routing metadata')
    if any(n.startswith('x-amz-') and n != 'x-amz-content-sha256' and n not in names for n in headers):
        raise Denied('Unsigned AWS metadata')
    # Prevent alternate host-based routing into the emulator's other services.
    host = headers['host'].split(':')[0]
    if host not in {os.environ.get('MINISTACK_HOST', 'ministack'), 'localhost', '127.0.0.1'}:
        raise Denied('Unsupported host')
    stamp = headers['x-amz-date']
    signed_at = datetime.strptime(stamp, '%Y%m%dT%H%M%SZ').replace(tzinfo=timezone.utc)
    if signed_at.strftime('%Y%m%dT%H%M%SZ') != stamp or stamp[:8] != date:
        raise Denied('Invalid signing date')
    if abs(((now or datetime.now(timezone.utc)) - signed_at).total_seconds()) > 300:
        raise Denied('Signature outside five-minute window')
    digest = hashlib.sha256(body).hexdigest()
    if 'x-amz-content-sha256' in headers and headers['x-amz-content-sha256'] != digest:
        raise Denied('Payload hash mismatch')
    secret = resolver(key)
    if not secret:
        raise Denied('Missing secret')
    request = AWSRequest(method='POST', url='http://' + headers['host'] + path,
                         data=body, headers={n: headers[n] for n in names})
    request.context['timestamp'] = stamp
    signer = SigV4Auth(Credentials(key, secret), service, region)
    if signer.signed_headers(signer.headers_to_sign(request)) != signed:
        raise Denied('Unsupported signed header set')
    canonical = signer.canonical_request(request)
    computed = signer.signature(signer.string_to_sign(request, canonical), request)
    if not hmac.compare_digest(computed, supplied):
        raise Denied('Signature mismatch')


async def reject(send, status=403):
    # Fixed response: no headers, bodies, credentials or resolver details logged.
    body = json.dumps({'__type': 'InvalidSignatureException',
                       'message': 'Request authentication failed'}).encode()
    await send({'type': 'http.response.start', 'status': status,
                'headers': [(b'content-type', b'application/x-amz-json-1.0'),
                            (b'content-length', str(len(body)).encode())]})
    await send({'type': 'http.response.body', 'body': body})


class SignatureGate:
    def __init__(self, downstream, resolver=resolve_secret):
        self.downstream, self.resolver = downstream, resolver

    async def __call__(self, scope, receive, send):
        if scope['type'] == 'lifespan':
            return await self.downstream(scope, receive, send)
        if scope['type'] != 'http':
            if scope['type'] == 'websocket':
                await send({'type': 'websocket.close', 'code': 1008})
            return
        if scope['method'] == 'GET' and scope['path'] == '/_ministack/health' and not scope.get('query_string'):
            return await self.downstream(scope, receive, send)
        try:
            async def read():
                chunks, size = [], 0
                while True:
                    frame = await receive()
                    if frame['type'] != 'http.request':
                        raise Denied('Disconnected')
                    part = frame.get('body', b'')
                    size += len(part)
                    if size > 2 * 1024 * 1024:
                        raise Denied('Request too large')
                    chunks.append(part)
                    if not frame.get('more_body', False):
                        return b''.join(chunks)
            body = await asyncio.wait_for(read(), timeout=10)
            verify(scope, body, self.resolver)
        except (ValueError, KeyError, UnicodeError, TimeoutError):
            return await reject(send)
        except Exception:
            # Dependency/import/resolver faults fail closed, but are not invalid credentials.
            return await reject(send, 503)
        consumed = False
        async def replay():
            nonlocal consumed
            if not consumed:
                consumed = True
                return {'type': 'http.request', 'body': body, 'more_body': False}
            return await receive()
        return await self.downstream(scope, replay, send)
