"""Least-privilege policy templates for the local wagering environment."""

import json
import os
from urllib.parse import unquote

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

from common import queue_attributes, require, sqs_client

POLICY_NAME = "wagering-queue-access"
USER_PATH = "/wagering/"
IDENTITIES = {
    "producer": "wagering-producer",
    "consumer": "wagering-consumer",
    "publisher": "wagering-outbox-publisher",
}


def iam_client():
    return boto3.client(
        "iam",
        endpoint_url=os.environ["AWS_ENDPOINT_URL"],
        region_name=os.environ.get("AWS_DEFAULT_REGION", "us-east-1"),
        aws_access_key_id=os.environ["AWS_ACCESS_KEY_ID"],
        aws_secret_access_key=os.environ["AWS_SECRET_ACCESS_KEY"],
        config=Config(connect_timeout=3, read_timeout=10,
                      retries={"max_attempts": 1, "mode": "standard"}),
    )


def policy_for(role, input_arn, output_arn):
    actions = ["sqs:GetQueueUrl", "sqs:GetQueueAttributes"]
    if role in ("producer", "publisher"):
        actions.append("sqs:SendMessage")
    elif role == "consumer":
        actions.extend(["sqs:ReceiveMessage", "sqs:DeleteMessage",
                        "sqs:ChangeMessageVisibility"])
    else:
        raise ValueError("Unknown SQS identity role")
    return {
        "Version": "2012-10-17",
        "Statement": [{
            "Effect": "Allow",
            "Action": actions,
            "Resource": output_arn if role == "publisher" else input_arn,
        }],
    }


def decode_policy(document):
    return json.loads(unquote(document)) if isinstance(document, str) else document


def queue_arns(client):
    input_url = client.get_queue_url(QueueName="wager-transactions.fifo")["QueueUrl"]
    output_url = client.get_queue_url(QueueName="wager-events.fifo")["QueueUrl"]
    return (queue_attributes(client, input_url)["QueueArn"],
            queue_attributes(client, output_url)["QueueArn"])


def verify_user_policy(client, username, expected):
    require(client.get_user(UserName=username)["User"]["Path"] == USER_PATH,
            f"Unexpected IAM user ownership: {username}")
    names = client.list_user_policies(UserName=username)
    attached = client.list_attached_user_policies(UserName=username)
    groups = client.list_groups_for_user(UserName=username)
    require(not names.get("IsTruncated") and names["PolicyNames"] == [POLICY_NAME],
            f"Unexpected inline policies for {username}")
    require(not attached.get("IsTruncated") and not attached["AttachedPolicies"],
            f"Unexpected managed policies for {username}")
    require(not groups.get("IsTruncated") and not groups["Groups"],
            f"Unexpected group memberships for {username}")
    stored = client.get_user_policy(UserName=username, PolicyName=POLICY_NAME)
    require(decode_policy(stored["PolicyDocument"]) == expected,
            f"Policy mismatch for {username}")


def provision_identities():
    iam = iam_client()
    input_arn, output_arn = queue_arns(sqs_client())
    for role, username in IDENTITIES.items():
        try:
            user = iam.get_user(UserName=username)["User"]
        except ClientError as error:
            if error.response["Error"]["Code"] != "NoSuchEntity":
                raise
            user = iam.create_user(UserName=username, Path=USER_PATH)["User"]
        require(user["Path"] == USER_PATH, f"Refusing to modify unrelated user {username}")
        policy = policy_for(role, input_arn, output_arn)
        iam.put_user_policy(UserName=username, PolicyName=POLICY_NAME,
                            PolicyDocument=json.dumps(policy))
        verify_user_policy(iam, username, policy)
        print(f"Provisioned IAM identity: {username}", flush=True)
    print("SQS IAM provisioning completed.", flush=True)
