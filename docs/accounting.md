# Double-Entry Accounting

Migration 002 adds immutable accounts, journals and postings. Every wallet
movement must have exactly one journal and two postings with equal positive
amounts and currency, one debit and one credit. The database checks the accounts
and sides against the wallet ledger, not merely the net sum.

| Wallet movement | Debit account | Credit account |
| --- | --- | --- |
| Positive OPENING | Opening clearing | Wallet liability |
| BET | Wallet liability | Game clearing |
| WIN or REFUND | Game clearing | Wallet liability |
| ROLLBACK | Opposite sides of its eligible original movement | Opposite sides of its eligible original movement |

Wallet balances represent a liability of the operator. The wallet ledger still
has exactly one entry per movement. Accounting postings do not add an extra
wallet movement. LOSS and rejections have no journal.

Clearing accounts hold no mutable global balance. Their positions are derived
from append-only postings, avoiding a shared balance row that serializes unrelated
wallets. A wallet liability account is created with its first financial movement.

Deferred constraints reject missing journals, partial postings, incorrect
currencies or amounts, wrong accounts and incorrect sides at commit. Database
permissions and triggers reject edits, deletes and truncation. Tests are included
in the isolated financial PostgreSQL suite described in persistence.md.
