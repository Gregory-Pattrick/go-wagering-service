"""Single listener: signature validation followed by MiniStack IAM/service dispatch."""
import os
from ministack.app import app as upstream
from signature_gate import SignatureGate

if os.environ.get('AUTH', '').lower() != 'true':
    raise RuntimeError('The strict broker requires AUTH=true')
app = SignatureGate(upstream)
