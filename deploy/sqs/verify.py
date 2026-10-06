"""Exercise the running emulator; leave business queue messages untouched."""

import json
from uuid import uuid4

from botocore.exceptions import ClientError

from common import QUEUE_PAIRS, queue_attributes, require, sqs_client


def verify_configuration(client):
    first_source_url = None
    for source_name, dlq_name in QUEUE_PAIRS:
        source_url = client.get_queue_url(QueueName=source_name)["QueueUrl"]
        dlq_url = client.get_queue_url(QueueName=dlq_name)["QueueUrl"]
        source = queue_attributes(client, source_url)
        dlq = queue_attributes(client, dlq_url)
        for name, attributes, retention in (
            (source_name, source, 345600), (dlq_name, dlq, 1209600)
        ):
            require(attributes.get("FifoQueue") == "true", f"Not FIFO: {name}")
            require(attributes.get("ContentBasedDeduplication") == "false", f"Wrong deduplication: {name}")
            require(int(attributes["VisibilityTimeout"]) == 30, f"Wrong visibility: {name}")
            require(int(attributes["MessageRetentionPeriod"]) == retention, f"Wrong retention: {name}")
            require(int(attributes["ReceiveMessageWaitTimeSeconds"]) == 10, f"Wrong long polling: {name}")
        redrive = json.loads(source["RedrivePolicy"])
        require(redrive["deadLetterTargetArn"] == dlq["QueueArn"], "Incorrect DLQ target")
        require(int(redrive["maxReceiveCount"]) == 5, "Incorrect redrive receive count")
        allowed = json.loads(dlq["RedriveAllowPolicy"])
        require(allowed["redrivePermission"] == "byQueue", "DLQ redrive is not restricted")
        require(allowed["sourceQueueArns"] == [source["QueueArn"]], "Incorrect allowed source")
        first_source_url = first_source_url or source_url
        print(f"PASS: FIFO attributes and redrive configuration for {source_name}", flush=True)
    return first_source_url


def expect_credentials_denied(client, queue_url, label):
    try:
        client.get_queue_attributes(QueueUrl=queue_url, AttributeNames=["QueueArn"])
    except ClientError as error:
        status = error.response["ResponseMetadata"]["HTTPStatusCode"]
        require(status in (401, 403), f"Unexpected failure instead of authentication denial: {status}")
    else:
        raise RuntimeError(f"Invalid credentials were accepted: {label}")
    print(f"PASS: {label} denied", flush=True)


def receive_one(client, queue_url):
    for _ in range(3):
        result = client.receive_message(
            QueueUrl=queue_url,
            MaxNumberOfMessages=1,
            WaitTimeSeconds=2,
            AttributeNames=["ApproximateReceiveCount"],
        )
        if result.get("Messages"):
            return result["Messages"][0]
    raise RuntimeError("Expected message was not delivered within the polling budget")


def verify_delivery(client):
    queue_url = client.create_queue(
        QueueName=f"wager-smoke-{uuid4().hex}.fifo",
        Attributes={"FifoQueue": "true", "ContentBasedDeduplication": "false", "VisibilityTimeout": "30"},
    )["QueueUrl"]
    try:
        body = '{"type":"InfrastructureSmokeTest"}'
        arguments = {
            "QueueUrl": queue_url,
            "MessageBody": body,
            "MessageGroupId": "smoke-wallet",
            "MessageDeduplicationId": uuid4().hex,
        }
        sent = client.send_message(**arguments)
        duplicate = client.send_message(**arguments)
        require(sent["MessageId"] == duplicate["MessageId"], "FIFO transport deduplication failed")
        received = receive_one(client, queue_url)
        require(received["Body"] == body, "Message body changed")
        require(received["MessageId"] == sent["MessageId"], "Unexpected message identity")
        hidden = client.receive_message(QueueUrl=queue_url, WaitTimeSeconds=1)
        require(not hidden.get("Messages"), "In-flight message was still visible")
        client.change_message_visibility(
            QueueUrl=queue_url, ReceiptHandle=received["ReceiptHandle"], VisibilityTimeout=0
        )
        redelivered = receive_one(client, queue_url)
        require(redelivered["MessageId"] == sent["MessageId"], "Redelivery changed message identity")
        require(int(redelivered["Attributes"]["ApproximateReceiveCount"]) >= 2, "No actual redelivery observed")
        client.delete_message(QueueUrl=queue_url, ReceiptHandle=redelivered["ReceiptHandle"])
        remaining = client.receive_message(QueueUrl=queue_url, WaitTimeSeconds=1)
        require(not remaining.get("Messages"), "Message remained after deletion")
        print("PASS: send, FIFO deduplication, visibility, redelivery and deletion", flush=True)
    finally:
        client.delete_queue(QueueUrl=queue_url)


def main():
    client = sqs_client()
    queue_url = verify_configuration(client)
    expect_credentials_denied(sqs_client(access_key="invalid-local-key"), queue_url, "unknown access key")
    expect_credentials_denied(sqs_client(secret_key="incorrect-local-secret"), queue_url, "incorrect secret")
    verify_delivery(client)
    print("SQS infrastructure verification passed.", flush=True)


if __name__ == "__main__":
    main()
