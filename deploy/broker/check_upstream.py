"""Reject an unexpected upstream implementation instead of assuming compatibility."""
import hashlib
import importlib.util
import json
from pathlib import Path

root = Path(next(iter(importlib.util.find_spec('ministack').submodule_search_locations)))
expected = json.loads(Path(__file__).with_name('upstream-sha256.json').read_text())
for relative, digest in expected.items():
    if hashlib.sha256((root / relative).read_bytes()).hexdigest() != digest:
        raise RuntimeError('Unreviewed MiniStack implementation: ' + relative)
print('PASS: pinned upstream source compatibility')
