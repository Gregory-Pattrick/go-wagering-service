"""Create local producer/consumer keys in separate least-privilege volumes."""

import json
import os
from pathlib import Path
from iam_policies import IDENTITIES, iam_client, policy_for, queue_arns, verify_user_policy
from common import require, sqs_client


def main():
    iam = iam_client()
    input_arn, output_arn = queue_arns(sqs_client())
    for role in ("consumer", "producer"):
        username = IDENTITIES[role]
        verify_user_policy(iam, username, policy_for(role, input_arn, output_arn))
        destination = Path(f"/{role}_credentials/{role}.json")
        response = iam.list_access_keys(UserName=username)
        require(not response.get("IsTruncated"), "Unexpected paginated key list")
        active = {key["AccessKeyId"] for key in response["AccessKeyMetadata"]
                  if key["Status"] == "Active"}
        if destination.exists():
            stored = json.loads(destination.read_text())
            if stored.get("accessKeyId") in active and stored.get("secretAccessKey"):
                print(f"{role} credentials already provisioned.", flush=True)
                continue
        key = iam.create_access_key(UserName=username)["AccessKey"]
        temporary = destination.with_suffix(".tmp")
        temporary.write_text(json.dumps({"accessKeyId": key["AccessKeyId"],
                                         "secretAccessKey": key["SecretAccessKey"]}))
        os.chmod(temporary, 0o444)
        temporary.replace(destination)
        print(f"Restricted {role} credentials provisioned.", flush=True)


if __name__ == "__main__":
    main()
