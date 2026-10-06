"""Test IAM policies with real emulator requests and isolated disposable resources."""

import json
from uuid import uuid4

from botocore.exceptions import ClientError

from common import queue_attributes, require, sqs_client
from iam_policies import (
    IDENTITIES, POLICY_NAME, USER_PATH, iam_client, policy_for, queue_arns,
    verify_user_policy,
)
from verify import receive_one


def expect_denied(label, operation):
    try:
        operation()
    except ClientError as error:
        code = error.response["Error"]["Code"]
        status = error.response["ResponseMetadata"]["HTTPStatusCode"]
        require(status == 403 and code in ("AccessDenied", "AccessDeniedException"),
                f"{label}: expected policy denial, got {code} / HTTP {status}")
    else:
        raise RuntimeError(f"Forbidden operation was allowed: {label}")
    print(f"PASS: denied {label}", flush=True)


def send(client, queue_url):
    return client.send_message(
        QueueUrl=queue_url, MessageBody='{"type":"IAMPolicySmokeTest"}',
        MessageGroupId="iam-smoke", MessageDeduplicationId=uuid4().hex,
    )


def main():
    iam, admin = iam_client(), sqs_client()
    input_arn, output_arn = queue_arns(admin)
    for role, username in IDENTITIES.items():
        verify_user_policy(iam, username, policy_for(role, input_arn, output_arn))
        print(f"PASS: exact policy and no extra permissions for {username}", flush=True)

    # Disposable identities exercise the same templates with disposable queue ARNs.
    # Even if a denial check unexpectedly succeeds, no business queue is affected.
    suffix = uuid4().hex
    created_users, created_keys, created_policies, created_queues = [], [], [], []
    try:
        urls = []
        for label in ("input", "output"):
            result = admin.create_queue(
                QueueName=f"wager-iam-{label}-{suffix}.fifo",
                Attributes={"FifoQueue": "true", "ContentBasedDeduplication": "false"},
            )
            urls.append(result["QueueUrl"])
            created_queues.append(result["QueueUrl"])
        input_url, output_url = urls
        test_input_arn = queue_attributes(admin, input_url)["QueueArn"]
        test_output_arn = queue_attributes(admin, output_url)["QueueArn"]
        clients = {}
        for role in IDENTITIES:
            username = f"wager-iam-{role}-{suffix}"
            iam.create_user(UserName=username, Path=USER_PATH)
            created_users.append(username)
            policy = policy_for(role, test_input_arn, test_output_arn)
            iam.put_user_policy(UserName=username, PolicyName=POLICY_NAME,
                                PolicyDocument=json.dumps(policy))
            created_policies.append(username)
            key = iam.create_access_key(UserName=username)["AccessKey"]
            created_keys.append((username, key["AccessKeyId"]))
            clients[role] = sqs_client(key["AccessKeyId"], key["SecretAccessKey"])

        producer, consumer, publisher = (clients[role] for role in IDENTITIES)
        for client, url in ((producer, input_url), (consumer, input_url), (publisher, output_url)):
            queue_attributes(client, url)
            name = url.rsplit("/", 1)[-1]
            client.get_queue_url(QueueName=name)
        print("PASS: each identity can inspect its own queue", flush=True)

        sent = send(producer, input_url)
        received = receive_one(consumer, input_url)
        require(sent["MessageId"] == received["MessageId"], "Consumer received wrong message")
        consumer.change_message_visibility(
            QueueUrl=input_url, ReceiptHandle=received["ReceiptHandle"], VisibilityTimeout=0)
        received = receive_one(consumer, input_url)

        expect_denied("producer receiving input", lambda: producer.receive_message(QueueUrl=input_url))
        expect_denied("producer deleting input messages", lambda: producer.delete_message(
            QueueUrl=input_url, ReceiptHandle=received["ReceiptHandle"]))
        expect_denied("producer changing visibility", lambda: producer.change_message_visibility(
            QueueUrl=input_url, ReceiptHandle=received["ReceiptHandle"], VisibilityTimeout=0))
        expect_denied("consumer sending input", lambda: send(consumer, input_url))
        expect_denied("producer sending output", lambda: send(producer, output_url))
        expect_denied("consumer receiving output", lambda: consumer.receive_message(QueueUrl=output_url))
        expect_denied("publisher sending input", lambda: send(publisher, input_url))
        expect_denied("publisher receiving output", lambda: publisher.receive_message(QueueUrl=output_url))
        for role, client in clients.items():
            own_url = output_url if role == "publisher" else input_url
            other_url = input_url if role == "publisher" else output_url
            expect_denied(f"{role} inspecting another queue", lambda c=client, u=other_url: queue_attributes(c, u))
            expect_denied(f"{role} changing queue configuration", lambda c=client, u=own_url:
                          c.set_queue_attributes(QueueUrl=u, Attributes={"VisibilityTimeout": "5"}))
            expect_denied(f"{role} purging queue", lambda c=client, u=own_url: c.purge_queue(QueueUrl=u))
            expect_denied(f"{role} deleting queue", lambda c=client, u=own_url: c.delete_queue(QueueUrl=u))

        consumer.delete_message(QueueUrl=input_url, ReceiptHandle=received["ReceiptHandle"])
        published = send(publisher, output_url)
        event = receive_one(admin, output_url)
        require(published["MessageId"] == event["MessageId"], "Publisher event missing")
        admin.delete_message(QueueUrl=output_url, ReceiptHandle=event["ReceiptHandle"])
        print("PASS: producer send, consumer receive/visibility/delete and publisher send", flush=True)
    finally:
        cleanup_errors = []
        operations = []
        for username, key_id in created_keys:
            operations.append(lambda u=username, k=key_id: iam.delete_access_key(UserName=u, AccessKeyId=k))
        for username in created_policies:
            operations.append(lambda u=username: iam.delete_user_policy(UserName=u, PolicyName=POLICY_NAME))
        for username in created_users:
            operations.append(lambda u=username: iam.delete_user(UserName=u))
        for url in created_queues:
            operations.append(lambda u=url: admin.delete_queue(QueueUrl=u))
        for operation in operations:
            try:
                operation()
            except ClientError as error:
                if error.response["Error"]["Code"] not in (
                    "NoSuchEntity", "AWS.SimpleQueueService.NonExistentQueue", "QueueDoesNotExist"
                ):
                    cleanup_errors.append(error.response["Error"]["Code"])
        require(not cleanup_errors, "IAM test cleanup failed: " + ", ".join(cleanup_errors))
    print("SQS IAM policy verification passed.", flush=True)
    print("LIMITATION: this verifies authorization, not SigV4 signature authentication.", flush=True)


if __name__ == "__main__":
    main()
