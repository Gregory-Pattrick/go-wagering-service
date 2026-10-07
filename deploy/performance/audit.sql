-- Read-only assertions against the isolated suite database.
DO $$
BEGIN
 IF EXISTS (
  SELECT 1 FROM wagering.wallets w
  LEFT JOIN LATERAL (
   SELECT coalesce(sum(CASE direction WHEN 'CREDIT' THEN amount_minor::numeric ELSE -amount_minor::numeric END),0) balance,
          count(*) entries, max(wallet_version) last_version
   FROM wagering.wallet_ledger_entries e WHERE e.wallet_id=w.id
  ) l ON true
  WHERE w.balance_minor<>l.balance OR w.version<>l.entries OR w.version<>l.last_version
 ) THEN RAISE EXCEPTION 'Wallet balance or version disagrees with ledger'; END IF;
 IF EXISTS (
  SELECT 1 FROM wagering.journals j LEFT JOIN wagering.postings p ON p.transaction_id=j.transaction_id
  GROUP BY j.transaction_id
  HAVING count(p.transaction_id)<>2 OR count(DISTINCT p.currency)<>1
   OR sum(CASE p.side WHEN 'CREDIT' THEN p.amount_minor::numeric ELSE -p.amount_minor::numeric END)<>0
 ) THEN RAISE EXCEPTION 'Unbalanced or incomplete accounting journal'; END IF;
 IF EXISTS (
  SELECT 1 FROM wagering.wallet_ledger_entries e
  LEFT JOIN wagering.journals j ON j.ledger_id=e.id AND j.transaction_id=e.transaction_id
  WHERE j.transaction_id IS NULL
 ) THEN RAISE EXCEPTION 'Ledger entry without journal'; END IF;
 IF EXISTS (
  SELECT 1 FROM wagering.wallets w LEFT JOIN LATERAL (
   SELECT coalesce(sum(CASE p.side WHEN 'CREDIT' THEN p.amount_minor::numeric ELSE -p.amount_minor::numeric END),0) balance
   FROM wagering.accounts a JOIN wagering.postings p ON p.account_id=a.id
   WHERE a.wallet_id=w.id AND a.role='WALLET_LIABILITY'
  ) accounting ON true WHERE accounting.balance<>w.balance_minor
 ) THEN RAISE EXCEPTION 'Wallet balance disagrees with liability account'; END IF;
 IF EXISTS (
  SELECT 1 FROM wagering.wager_transactions WHERE kind<>'OPENING'
  GROUP BY provider_id,external_id HAVING count(*)>1
 ) OR EXISTS (
  SELECT 1 FROM wagering.wager_transactions WHERE kind<>'OPENING'
  GROUP BY provider_id,idempotency_key HAVING count(*)>1
 ) THEN RAISE EXCEPTION 'Duplicate financial identity'; END IF;
 IF EXISTS (
  SELECT 1 FROM wagering.wager_transactions t JOIN wagering.wallet_ledger_entries e ON e.transaction_id=t.id
  WHERE t.status<>'PROCESSED'
 ) THEN RAISE EXCEPTION 'Non-processed transaction moved money'; END IF;
END $$;
-- Both publishers must finish durable work; do not use approximate SQS counts as proof.
DO $$
BEGIN
 FOR attempt IN 1..180 LOOP
  IF NOT EXISTS(SELECT 1 FROM wagering.outbox WHERE published_at IS NULL) THEN RETURN; END IF;
  PERFORM pg_sleep(0.5);
 END LOOP;
 RAISE EXCEPTION 'Outbox did not drain within 90 seconds';
END $$;
SELECT 'PASS: identities, ledger, double-entry accounting and outbox drain' AS result;
