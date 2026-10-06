"""Persist one least-privilege local publisher key without printing secrets."""

import json
import os
from pathlib import Path

from iam_policies import (
    IDENTITIES, iam_client, policy_for, queue_arns, verify_user_policy,
)
from common import require, sqs_client


def main():
    iam = iam_client()
    username = IDENTITIES["publisher"]
    input_arn, output_arn = queue_arns(sqs_client())
    verify_user_policy(iam, username, policy_for("publisher", input_arn, output_arn))
    destination = Path("/credentials/publisher.json")
    response = iam.list_access_keys(UserName=username)
    require(not response.get("IsTruncated"), "Unexpected paginated access keys")
    active = {item["AccessKeyId"] for item in response["AccessKeyMetadata"]
              if item["Status"] == "Active"}
    if destination.exists():
        existing = json.loads(destination.read_text())
        if existing.get("accessKeyId") in active and existing.get("secretAccessKey"):
            print("Publisher credentials already provisioned.", flush=True)
            return
    # Never delete or rotate unknown keys automatically. A full key quota fails
    # explicitly and can be investigated without breaking a running publisher.
    key = iam.create_access_key(UserName=username)["AccessKey"]
    temporary = destination.with_suffix(".tmp")
    temporary.write_text(json.dumps({
        "accessKeyId": key["AccessKeyId"],
        "secretAccessKey": key["SecretAccessKey"],
    }))
    # The worker is UID 65532. The named volume is mounted only where needed;
    # runtime gets a read-only mount. These are local fixtures, not production IAM.
    os.chmod(temporary, 0o444)
    temporary.replace(destination)
    print("Least-privilege publisher credentials provisioned.", flush=True)


if __name__ == "__main__":
    main()
