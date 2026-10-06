"""Real local SQS/API smoke test. Never purges queues or prints credentials."""

import json
import os
import time
import uuid
import urllib.request
import urllib.parse
import urllib.error
from datetime import datetime, timezone
from pathlib import Path

from common import require, sqs_client

API = os.environ["API_BASE_URL"]
TOKEN_URL = os.environ["KEYCLOAK_TOKEN_URL"]


def token(client, secret):
    data = urllib.parse.urlencode({"grant_type": "client_credentials",
                                   "client_id": client, "client_secret": secret}).encode()
    request = urllib.request.Request(TOKEN_URL, data=data,
                                     headers={"Content-Type": "application/x-www-form-urlencoded"})
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)["access_token"]


def api(method, path, access_token, body=None, key=None):
    headers = {"Authorization": "Bearer " + access_token}
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode()
    if key:
        headers["Idempotency-Key"] = key
    request = urllib.request.Request(API + path, data=data, headers=headers, method=method)
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)


def eventually(check, description, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = check()
        if result:
            return result
        time.sleep(0.5)
    raise RuntimeError("Timed out: " + description)


def pending(admin, queue):
    attrs = admin.get_queue_attributes(QueueUrl=queue, AttributeNames=[
        "ApproximateNumberOfMessages", "ApproximateNumberOfMessagesNotVisible",
        "ApproximateNumberOfMessagesDelayed"])["Attributes"]
    return sum(int(attrs.get(name, 0)) for name in (
        "ApproximateNumberOfMessages", "ApproximateNumberOfMessagesNotVisible",
        "ApproximateNumberOfMessagesDelayed"))


def main():
    admin = sqs_client()  # Test observation only; never used for producer sends.
    source = admin.get_queue_url(QueueName="wager-transactions.fifo")["QueueUrl"]
    dlq = admin.get_queue_url(QueueName="wager-transactions-dlq.fifo")["QueueUrl"]
    require(pending(admin, source) == 0 and pending(admin, dlq) == 0,
            "Smoke test requires empty input and input DLQ. Inspect existing messages; do not purge them.")
    credentials = json.loads(Path("/credentials/producer.json").read_text())
    producer = sqs_client(credentials["accessKeyId"], credentials["secretAccessKey"])
    internal = token("wallet-service", os.environ["WALLET_CLIENT_SECRET"])
    provider = token("provider-a", os.environ["PROVIDER_A_CLIENT_SECRET"])
    player = str(uuid.uuid4())
    wallet = api("POST", "/wallets", internal, {"playerId": player,
                 "initialBalance": {"amount": "100.00", "currency": "BRL"}})
    wallet_id = wallet["id"]
    external = str(uuid.uuid4())
    key = "provider-a:" + external
    data = {"providerId": "provider-a", "externalTransactionId": external,
            "idempotencyKey": key, "playerId": player, "walletId": wallet_id,
            "roundId": "sqs-smoke-round", "gameId": "sqs-smoke-game", "kind": "BET",
            "money": {"amount": "80.00", "currency": "BRL"}}
    envelope = {"messageId": str(uuid.uuid4()), "type": "WagerTransactionRequested",
                "occurredAt": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
                "data": data}

    def send(body):
        producer.send_message(QueueUrl=source, MessageBody=body, MessageGroupId=wallet_id,
                              MessageDeduplicationId=str(uuid.uuid4()))

    def lookup(external_id, expected):
        try:
            result = api("GET", "/providers/provider-a/wagering/transactions/" + external_id, provider)
        except urllib.error.HTTPError as error:
            if error.code == 404:
                return None
            raise
        require(result["status"] == expected, "Unexpected financial status")
        return result

    # This operation first arrives exclusively by SQS.
    send(json.dumps(envelope))
    first = eventually(lambda: lookup(external, "PROCESSED"), "SQS financial commit")
    require(first["balance"]["amount"] == "20.00", "SQS BET balance mismatch")
    http_body = {name: value for name, value in data.items() if name != "idempotencyKey"}
    replay = api("POST", "/wagering/transactions", provider, http_body, key)
    require(replay["idempotentReplay"] and replay["transactionId"] == first["transactionId"],
            "HTTP did not replay the SQS result")
    # Different transport deduplication IDs force broker acceptance of each send.
    send(json.dumps(envelope))
    envelope["messageId"] = str(uuid.uuid4())
    send(json.dumps(envelope))
    eventually(lambda: pending(admin, source) == 0, "duplicate delivery acknowledgment")

    rejected_id = str(uuid.uuid4())
    rejected_data = dict(data, externalTransactionId=rejected_id,
                         idempotencyKey="provider-a:" + rejected_id,
                         money={"amount": "30.00", "currency": "BRL"})
    rejection = dict(envelope, messageId=str(uuid.uuid4()), data=rejected_data)
    send(json.dumps(rejection))
    result = eventually(lambda: lookup(rejected_id, "REJECTED"), "business rejection")
    require(result["failureCode"] == "INSUFFICIENT_FUNDS", "Unexpected rejection code")
    eventually(lambda: pending(admin, source) == 0, "business rejection acknowledgment")

    poison = '{"messageId":"' + str(uuid.uuid4()) + '","broken":'
    send(poison)

    def observe_dlq():
        messages = admin.receive_message(QueueUrl=dlq, MaxNumberOfMessages=1,
                                         WaitTimeSeconds=1).get("Messages", [])
        if not messages:
            return False
        message = messages[0]
        require(message["Body"] == poison,
                "Unexpected DLQ message; left undeleted for inspection")
        admin.delete_message(QueueUrl=dlq, ReceiptHandle=message["ReceiptHandle"])
        return True

    eventually(observe_dlq, "actual broker redrive to DLQ", timeout=150)
    final = api("GET", "/wallets/" + wallet_id, internal)
    require(final["balance"]["amount"] == "20.00" and final["version"] == 2,
            "Duplicate, rejection or malformed message changed wallet")
    check = api("POST", "/wallets/" + wallet_id + "/reconciliation", internal)
    require(check["consistent"] and check["difference"]["amount"] == "0.00"
            and check["checkedEntries"] == 2, "Reconciliation failed")
    print("PASS: SQS processing, HTTP replay, duplicate deliveries, business rejection and observed DLQ redrive.")
    print("Wallet ID: " + wallet_id)


if __name__ == "__main__":
    main()
