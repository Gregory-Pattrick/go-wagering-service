"""Local SQS provisioning helpers; never used by the Go application."""

import os

import boto3
from botocore.config import Config

QUEUE_PAIRS = (
    ("wager-transactions.fifo", "wager-transactions-dlq.fifo"),
    ("wager-events.fifo", "wager-events-dlq.fifo"),
)


def sqs_client(access_key=None, secret_key=None):
    return boto3.client(
        "sqs",
        endpoint_url=os.environ["AWS_ENDPOINT_URL"],
        region_name=os.environ.get("AWS_DEFAULT_REGION", "us-east-1"),
        aws_access_key_id=access_key or os.environ["AWS_ACCESS_KEY_ID"],
        aws_secret_access_key=secret_key or os.environ["AWS_SECRET_ACCESS_KEY"],
        config=Config(
            signature_version="v4",
            connect_timeout=3,
            read_timeout=10,
            retries={"max_attempts": 1, "mode": "standard"},
        ),
    )


def queue_attributes(client, queue_url):
    return client.get_queue_attributes(
        QueueUrl=queue_url, AttributeNames=["All"]
    )["Attributes"]


def require(condition, message):
    if not condition:
        raise RuntimeError(message)
