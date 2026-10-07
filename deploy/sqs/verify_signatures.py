"""Real HTTP signature checks against the strict broker, using disposable IAM keys."""
from datetime import datetime, timedelta, timezone
import json
import os
import urllib.error
import urllib.request
from uuid import uuid4

from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials
from common import require, sqs_client
from iam_policies import iam_client
from verify import expect_credentials_denied, verify_configuration


def raw(headers, body, path='/'):
    request = urllib.request.Request(os.environ['AWS_ENDPOINT_URL'].rstrip('/') + path,
                                    data=body, headers=headers, method='POST')
    try:
        response = urllib.request.urlopen(request, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.read()


def signed(key, secret, body, stamp=None):
    request = AWSRequest(method='POST', url=os.environ['AWS_ENDPOINT_URL'].rstrip('/') + '/',
                         data=body, headers={'Content-Type': 'application/x-amz-json-1.0',
                         'X-Amz-Target': 'AmazonSQS.GetQueueUrl'})
    signer = SigV4Auth(Credentials(key, secret), 'sqs', 'us-east-1')
    if stamp:
        request.context['timestamp'] = stamp
        request.headers['X-Amz-Date'] = stamp
        signature = signer.signature(signer.string_to_sign(request, signer.canonical_request(request)), request)
        signer._inject_signature_to_request(request, signature)
    else:
        signer.add_auth(request)
    return dict(request.headers.items())


def denied(label, headers, body, path='/'):
    status, data = raw(headers, body, path)
    require(status == 403, f'{label}: expected HTTP 403, got {status}')
    require(json.loads(data).get('__type') == 'InvalidSignatureException', f'{label}: wrong denial boundary')
    print('PASS: signature gate denied ' + label, flush=True)


def main():
    admin = sqs_client()
    queue = verify_configuration(admin)
    body = json.dumps({'QueueName': 'wager-transactions.fifo'}).encode()
    key, secret = os.environ['AWS_ACCESS_KEY_ID'], os.environ['AWS_SECRET_ACCESS_KEY']
    require(raw(signed(key, secret, body), body)[0] == 200, 'Valid signed request failed')
    denied('missing signature', {'Content-Type': 'application/x-amz-json-1.0', 'X-Amz-Target': 'AmazonSQS.GetQueueUrl'}, body)
    denied('incorrect administrator secret', signed(key, 'wrong-secret', body), body)
    denied('unknown key', signed('UNKNOWNKEY', secret, body), body)
    headers = signed(key, secret, body)
    denied('modified payload', headers, body + b' ')
    headers['X-Amz-Target'] = 'AmazonSQS.ListQueues'
    denied('modified operation', headers, body)
    for offset in (-600, 600):
        stamp = (datetime.now(timezone.utc) + timedelta(seconds=offset)).strftime('%Y%m%dT%H%M%SZ')
        denied('expired or future signature', signed(key, secret, body, stamp), body)
    denied('administrative bypass route', signed(key, secret, body), body, '/_ministack/reset')

    iam = iam_client()
    username = 'wager-signature-' + uuid4().hex
    access = None
    created = False
    try:
        iam.create_user(UserName=username, Path='/wagering/')
        created = True
        access = iam.create_access_key(UserName=username)['AccessKey']
        iam.put_user_policy(UserName=username, PolicyName='signature-test', PolicyDocument=json.dumps({
            'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': ['sqs:GetQueueAttributes'],
            'Resource': admin.get_queue_attributes(QueueUrl=queue, AttributeNames=['QueueArn'])['Attributes']['QueueArn']}]}))
        client = sqs_client(access['AccessKeyId'], access['SecretAccessKey'])
        client.get_queue_attributes(QueueUrl=queue, AttributeNames=['QueueArn'])
        expect_credentials_denied(sqs_client(access['AccessKeyId'], 'incorrect-secret'), queue, 'incorrect IAM secret')
        iam.update_access_key(UserName=username, AccessKeyId=access['AccessKeyId'], Status='Inactive')
        expect_credentials_denied(client, queue, 'inactive IAM key')
    finally:
        if access:
            iam.delete_access_key(UserName=username, AccessKeyId=access['AccessKeyId'])
        if created:
            # If policy creation failed, attempt only cleanup of known resources.
            policies = iam.list_user_policies(UserName=username)['PolicyNames']
            if 'signature-test' in policies:
                iam.delete_user_policy(UserName=username, PolicyName='signature-test')
            iam.delete_user(UserName=username)
    print('PASS: live broker signatures, request integrity and inactive-key rejection', flush=True)


if __name__ == '__main__':
    main()
