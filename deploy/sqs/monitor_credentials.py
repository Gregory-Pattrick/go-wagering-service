"""Provision a metadata-only identity for API readiness and queue-depth probes."""

import json
import os
from pathlib import Path
from botocore.exceptions import ClientError
from common import require, sqs_client, QUEUE_PAIRS, queue_attributes
from iam_policies import iam_client, USER_PATH, POLICY_NAME, verify_user_policy


def main():
    iam, sqs = iam_client(), sqs_client()
    username = "wagering-monitor"
    resources = []
    for pair in QUEUE_PAIRS:
        for name in pair:
            url = sqs.get_queue_url(QueueName=name)["QueueUrl"]
            resources.append(queue_attributes(sqs, url)["QueueArn"])
    try:
        user = iam.get_user(UserName=username)["User"]
    except ClientError as error:
        if error.response["Error"]["Code"] != "NoSuchEntity":
            raise
        user = iam.create_user(UserName=username, Path=USER_PATH)["User"]
    require(user["Path"] == USER_PATH, "Refusing unrelated monitor identity")
    policy = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow",
              "Action": ["sqs:GetQueueAttributes"], "Resource": resources}]}
    iam.put_user_policy(UserName=username, PolicyName=POLICY_NAME,
                        PolicyDocument=json.dumps(policy))
    verify_user_policy(iam, username, policy)
    destination = Path("/credentials/monitor.json")
    response = iam.list_access_keys(UserName=username)
    require(not response.get("IsTruncated"), "Unexpected key pagination")
    active = {key["AccessKeyId"] for key in response["AccessKeyMetadata"]
              if key["Status"] == "Active"}
    if destination.exists():
        saved = json.loads(destination.read_text())
        if saved.get("accessKeyId") in active and saved.get("secretAccessKey"):
            print("Monitor credentials already provisioned.", flush=True)
            return
    key = iam.create_access_key(UserName=username)["AccessKey"]
    temporary = destination.with_suffix(".tmp")
    temporary.write_text(json.dumps({"accessKeyId": key["AccessKeyId"],
                                     "secretAccessKey": key["SecretAccessKey"]}))
    os.chmod(temporary, 0o444)
    temporary.replace(destination)
    print("Metadata-only monitor credentials provisioned.", flush=True)


if __name__ == "__main__":
    main()
