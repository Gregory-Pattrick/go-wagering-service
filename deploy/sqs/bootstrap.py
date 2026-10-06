"""Create or reconcile local FIFO queues without deleting existing messages."""

import json

from botocore.exceptions import ClientError

from common import QUEUE_PAIRS, queue_attributes, require, sqs_client
from iam_policies import provision_identities


def ensure_queue(client, name, retention):
    attributes = {
        "FifoQueue": "true",
        "ContentBasedDeduplication": "false",
        "VisibilityTimeout": "30",
        "ReceiveMessageWaitTimeSeconds": "10",
        "MessageRetentionPeriod": str(retention),
    }
    try:
        queue_url = client.get_queue_url(QueueName=name)["QueueUrl"]
    except ClientError as error:
        code = error.response["Error"]["Code"]
        if code not in ("AWS.SimpleQueueService.NonExistentQueue", "QueueDoesNotExist"):
            raise
        queue_url = client.create_queue(
            QueueName=name, Attributes=attributes
        )["QueueUrl"]
    else:
        current = queue_attributes(client, queue_url)
        require(current.get("FifoQueue") == "true", f"Existing queue is not FIFO: {name}")
        mutable = {key: value for key, value in attributes.items() if key != "FifoQueue"}
        client.set_queue_attributes(QueueUrl=queue_url, Attributes=mutable)
    return queue_url


def main():
    client = sqs_client()
    for source_name, dlq_name in QUEUE_PAIRS:
        dlq_url = ensure_queue(client, dlq_name, 1209600)
        source_url = ensure_queue(client, source_name, 345600)
        dlq_arn = queue_attributes(client, dlq_url)["QueueArn"]
        source_arn = queue_attributes(client, source_url)["QueueArn"]
        client.set_queue_attributes(
            QueueUrl=source_url,
            Attributes={"RedrivePolicy": json.dumps({
                "deadLetterTargetArn": dlq_arn,
                "maxReceiveCount": "5",
            })},
        )
        client.set_queue_attributes(
            QueueUrl=dlq_url,
            Attributes={"RedriveAllowPolicy": json.dumps({
                "redrivePermission": "byQueue",
                "sourceQueueArns": [source_arn],
            })},
        )
        print(f"Provisioned {source_name} -> {dlq_name}", flush=True)
    provision_identities()
    print("SQS provisioning completed.", flush=True)


if __name__ == "__main__":
    main()
