"""Check live dependency failures; container stop/start is driven by the operator."""

import json
import re
import sys
import time
import uuid
import urllib.request
import urllib.error
from pathlib import Path
from smoke_consumer import api, token, eventually
import os

TARGETS = {"api": "http://app:9090", "workers": "http://workers:9090",
           "consumer": "http://consumer:9090"}
FIXTURE = Path("/results/telemetry-smoke.json")


def get(url):
    try:
        with urllib.request.urlopen(url, timeout=5) as response:
            return response.status, response.read().decode()
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode()
    except (urllib.error.URLError, TimeoutError):
        return 0, ""


def check(mode):
    ready_code = 200 if mode == "healthy" else 503
    for name, base in TARGETS.items():
        status, _ = get(base + "/health/live")
        if status != 200:
            return False
        status, body = get(base + "/health/ready")
        if status != ready_code:
            return False
        dependencies = json.loads(body).get("dependencies") or {}
        if mode == "postgres-down" and dependencies.get("postgres") is not False:
            return False
        sqs_states = [value for key, value in dependencies.items() if key.startswith("sqs_")]
        if mode == "sqs-down" and (not sqs_states or any(sqs_states)):
            return False
        status, metrics = get(base + "/metrics")
        if status != 200 or "wagering_dependency_up" not in metrics:
            return False
        if mode == "postgres-down" and name == "api":
            if 'wagering_database_state{measure="outbox_pending"} NaN' not in metrics:
                return False
    status, _ = get("http://app:8080/health/ready")
    return status == ready_code


def main():
    mode = sys.argv[1] if len(sys.argv) == 2 else "healthy"
    if mode not in ("healthy", "sqs-down", "postgres-down"):
        raise RuntimeError("Use healthy, sqs-down or postgres-down")
    eventually(lambda: check(mode), "dependency state " + mode, timeout=90)
    if mode == "sqs-down":
        internal = token("wallet-service", os.environ["WALLET_CLIENT_SECRET"])
        provider = token("provider-a", os.environ["PROVIDER_A_CLIENT_SECRET"])
        wallet = api("POST", "/wallets", internal, {"playerId": str(uuid.uuid4()),
                     "initialBalance": {"amount": "1.00", "currency": "BRL"}})
        external = str(uuid.uuid4())
        result = api("POST", "/wagering/transactions", provider, {
            "providerId": "provider-a", "externalTransactionId": external,
            "playerId": wallet["playerId"], "walletId": wallet["id"],
            "roundId": "telemetry-smoke", "gameId": "telemetry-smoke", "kind": "BET",
            "money": {"amount": "0.25", "currency": "BRL"}}, "provider-a:" + external)
        if result["status"] != "PROCESSED" or result["balance"]["amount"] != "0.75":
            raise RuntimeError("SQS outage changed the financial result")
        FIXTURE.write_text(json.dumps({"walletId": wallet["id"]}))
    if mode == "healthy" and FIXTURE.exists():
        internal = token("wallet-service", os.environ["WALLET_CLIENT_SECRET"])
        wallet_id = json.loads(FIXTURE.read_text())["walletId"]
        result = api("POST", "/wallets/" + wallet_id + "/reconciliation", internal)
        if not result["consistent"] or result["checkedEntries"] != 2:
            raise RuntimeError("Recovery reconciliation failed")
        def drained():
            _, body = get(TARGETS["api"] + "/metrics")
            return re.search(r'^wagering_database_state\{measure="outbox_pending"\} 0(?:\.0)?$', body, re.M)
        eventually(drained, "outbox publication after SQS recovery", timeout=90)
    print("PASS: telemetry " + mode + ", liveness preserved and dependency state verified.")


if __name__ == "__main__":
    main()
