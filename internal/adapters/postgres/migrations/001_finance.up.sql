CREATE TABLE wagering.wallets (
 id uuid PRIMARY KEY CHECK (id <> '00000000-0000-0000-0000-000000000000'),
 player_id uuid NOT NULL CHECK (player_id <> '00000000-0000-0000-0000-000000000000'),
 currency text NOT NULL CHECK (currency IN ('BRL','USD')),
 opening_balance_minor bigint NOT NULL CHECK (opening_balance_minor >= 0),
 balance_minor bigint NOT NULL CHECK (balance_minor >= 0),
 version bigint NOT NULL CHECK (version > 0),
 created_at timestamptz NOT NULL CHECK(isfinite(created_at)),
 updated_at timestamptz NOT NULL CHECK (isfinite(updated_at) AND updated_at >= created_at),
 UNIQUE(player_id,currency)
);
CREATE TABLE wagering.wager_transactions (
 id uuid PRIMARY KEY,
 external_id text NOT NULL DEFAULT '', provider_id text NOT NULL DEFAULT '',
 idempotency_key text NOT NULL DEFAULT '', payload_hash text NOT NULL DEFAULT '',
 wallet_id uuid NOT NULL, player_id uuid NOT NULL,
 round_id text NOT NULL DEFAULT '', game_id text NOT NULL DEFAULT '',
 kind text NOT NULL CHECK (kind IN ('OPENING','BET','WIN','LOSS','REFUND','ROLLBACK')),
 amount_minor bigint NOT NULL, currency text NOT NULL CHECK (currency IN ('BRL','USD')),
 reference_external_id text NOT NULL DEFAULT '',
 resolved_reference_id uuid REFERENCES wagering.wager_transactions(id),
 status text NOT NULL CHECK (status IN ('PENDING','PENDING_REFERENCE','PROCESSED','REJECTED','FAILED')),
 failure_code text NOT NULL DEFAULT '',
 result_balance_minor bigint CHECK (result_balance_minor >= 0), result_version bigint CHECK (result_version > 0),
 reference_waited boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL CHECK (updated_at >= created_at),
 CHECK ((kind='LOSS' AND amount_minor=0) OR (kind<>'LOSS' AND amount_minor>0)),
 CHECK ((kind='OPENING' AND external_id='' AND provider_id='' AND idempotency_key='' AND payload_hash='' AND round_id='' AND game_id='' AND reference_external_id='' AND resolved_reference_id IS NULL)
 OR (kind<>'OPENING' AND external_id<>'' AND provider_id<>'' AND idempotency_key<>'' AND payload_hash ~ '^[0-9a-f]{64}$' AND round_id<>'' AND game_id<>'')),
 CHECK ((kind IN ('REFUND','ROLLBACK') AND reference_external_id<>'') OR (kind='WIN') OR (kind IN ('OPENING','BET','LOSS') AND reference_external_id='')),
 CHECK (reference_external_id='' OR reference_external_id<>external_id),
 CHECK (resolved_reference_id IS NULL OR (reference_external_id<>'' AND resolved_reference_id<>id)),
 CHECK ((result_balance_minor IS NULL)=(result_version IS NULL)),
 CHECK ((status IN ('PENDING','PENDING_REFERENCE') AND failure_code='' AND result_version IS NULL AND resolved_reference_id IS NULL)
 OR (status='PROCESSED' AND failure_code='' AND result_version IS NOT NULL AND (reference_external_id='' OR resolved_reference_id IS NOT NULL))
 OR (status='REJECTED' AND failure_code IN ('INSUFFICIENT_FUNDS','REVERSAL_INSUFFICIENT_FUNDS','REFERENCE_NOT_FOUND','REFERENCE_NOT_PROCESSED','REFERENCE_MISMATCH','REFERENCE_KIND_INVALID','ALREADY_REVERSED','WALLET_NOT_FOUND','WALLET_MISMATCH','BALANCE_OVERFLOW'))
 OR (status='FAILED' AND failure_code='PERMANENT_INFRASTRUCTURE_FAILURE' AND result_version IS NULL)),
 CHECK (status<>'PENDING_REFERENCE' OR (reference_external_id<>'' AND reference_waited)),
 CHECK (kind<>'OPENING' OR status<>'PROCESSED' OR (result_version=1 AND result_balance_minor=amount_minor))
);
-- No wallet FK here: an accepted missing-wallet rejection must remain auditable.
CREATE UNIQUE INDEX transaction_key ON wagering.wager_transactions(provider_id,idempotency_key) WHERE kind<>'OPENING';
CREATE UNIQUE INDEX transaction_external ON wagering.wager_transactions(provider_id,external_id) WHERE kind<>'OPENING';
CREATE UNIQUE INDEX one_opening ON wagering.wager_transactions(wallet_id) WHERE kind='OPENING';
CREATE UNIQUE INDEX one_compensation ON wagering.wager_transactions(resolved_reference_id) WHERE status='PROCESSED' AND kind IN ('REFUND','ROLLBACK');
CREATE INDEX transactions_wallet ON wagering.wager_transactions(wallet_id,created_at,id);
CREATE INDEX transactions_pending ON wagering.wager_transactions(updated_at,id) WHERE status IN ('PENDING','PENDING_REFERENCE');
CREATE TABLE wagering.wallet_ledger_entries (
 id uuid PRIMARY KEY, wallet_id uuid NOT NULL REFERENCES wagering.wallets(id),
 transaction_id uuid NOT NULL REFERENCES wagering.wager_transactions(id),
 direction text NOT NULL CHECK (direction IN ('DEBIT','CREDIT')),
 amount_minor bigint NOT NULL CHECK (amount_minor>0), currency text NOT NULL CHECK(currency IN ('BRL','USD')),
 balance_before bigint NOT NULL CHECK(balance_before>=0), balance_after bigint NOT NULL CHECK(balance_after>=0),
 wallet_version bigint NOT NULL CHECK(wallet_version>0), created_at timestamptz NOT NULL,
 UNIQUE(wallet_id,transaction_id), UNIQUE(transaction_id), UNIQUE(wallet_id,wallet_version),
 CHECK (balance_after::numeric=balance_before::numeric + CASE direction WHEN 'CREDIT' THEN amount_minor::numeric ELSE -amount_minor::numeric END)
);
CREATE TABLE wagering.outbox (
 event_id uuid PRIMARY KEY, transaction_id uuid NOT NULL REFERENCES wagering.wager_transactions(id),
 event_type text NOT NULL CHECK(event_type IN ('WagerTransactionProcessed','WagerTransactionRejected','WagerTransactionPendingReference','WalletBalanceChanged')),
 aggregate_id uuid NOT NULL REFERENCES wagering.wallets(id) DEFERRABLE INITIALLY DEFERRED,
 payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object'), occurred_at timestamptz NOT NULL,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0), next_attempt_at timestamptz NOT NULL,
 lease_owner text, lease_until timestamptz, published_at timestamptz,
 UNIQUE(transaction_id,event_type), CHECK ((lease_owner IS NULL)=(lease_until IS NULL))
);
-- Events for a missing-wallet rejection still use the requested wallet identity.
ALTER TABLE wagering.outbox DROP CONSTRAINT outbox_aggregate_id_fkey;
CREATE INDEX outbox_ready ON wagering.outbox(next_attempt_at,event_id) WHERE published_at IS NULL;
CREATE TABLE wagering.inbox (
 consumer_name text NOT NULL, message_id text NOT NULL, payload_hash text NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'),
 transaction_id uuid NOT NULL REFERENCES wagering.wager_transactions(id), received_at timestamptz NOT NULL,
 completed_at timestamptz NOT NULL CHECK(completed_at>=received_at), PRIMARY KEY(consumer_name,message_id)
);
CREATE TABLE wagering.transaction_work (
 transaction_id uuid PRIMARY KEY REFERENCES wagering.wager_transactions(id),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0), next_attempt_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL, lease_owner text, lease_until timestamptz,
 CHECK ((lease_owner IS NULL)=(lease_until IS NULL))
);
CREATE INDEX work_ready ON wagering.transaction_work(next_attempt_at,transaction_id);
CREATE FUNCTION wagering.deny_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'immutable financial record' USING ERRCODE='23514'; END $$;
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['wallet_ledger_entries','inbox'] LOOP
 EXECUTE format('CREATE TRIGGER immutable_rows BEFORE UPDATE OR DELETE ON wagering.%I FOR EACH ROW EXECUTE FUNCTION wagering.deny_mutation()',t);
 EXECUTE format('CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON wagering.%I FOR EACH STATEMENT EXECUTE FUNCTION wagering.deny_mutation()',t);
 END LOOP;
END $$;
CREATE FUNCTION wagering.guard_wallet() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.version<>1 OR NEW.balance_minor<>NEW.opening_balance_minor OR NEW.updated_at<>NEW.created_at THEN RAISE EXCEPTION 'invalid wallet opening' USING ERRCODE='23514'; END IF;
 ELSE
  IF (NEW.id,NEW.player_id,NEW.currency,NEW.opening_balance_minor,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.player_id,OLD.currency,OLD.opening_balance_minor,OLD.created_at)
   OR NEW.balance_minor=OLD.balance_minor OR NEW.version::numeric<>OLD.version::numeric+1 OR NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'invalid wallet mutation' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_wallet BEFORE INSERT OR UPDATE ON wagering.wallets FOR EACH ROW EXECUTE FUNCTION wagering.guard_wallet();
CREATE FUNCTION wagering.guard_transaction() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF OLD.status IN ('PROCESSED','REJECTED','FAILED') THEN RAISE EXCEPTION 'terminal transaction' USING ERRCODE='23514'; END IF;
  IF (to_jsonb(NEW)-ARRAY['status','failure_code','result_balance_minor','result_version','resolved_reference_id','reference_waited','updated_at']) IS DISTINCT FROM
   (to_jsonb(OLD)-ARRAY['status','failure_code','result_balance_minor','result_version','resolved_reference_id','reference_waited','updated_at'])
   OR NEW.updated_at<OLD.updated_at OR (OLD.status='PENDING_REFERENCE' AND NEW.status='PENDING') THEN RAISE EXCEPTION 'invalid transaction mutation' USING ERRCODE='23514'; END IF;
  NEW.reference_waited := OLD.reference_waited OR NEW.status='PENDING_REFERENCE';
 ELSE NEW.reference_waited := NEW.status='PENDING_REFERENCE';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_transaction BEFORE INSERT OR UPDATE ON wagering.wager_transactions FOR EACH ROW EXECUTE FUNCTION wagering.guard_transaction();
CREATE FUNCTION wagering.guard_ledger() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE w wagering.wallets; prior bigint;
BEGIN
 SELECT * INTO STRICT w FROM wagering.wallets WHERE id=NEW.wallet_id FOR UPDATE;
 IF (w.currency,w.balance_minor,w.version) IS DISTINCT FROM (NEW.currency,NEW.balance_after,NEW.wallet_version) THEN RAISE EXCEPTION 'ledger does not match locked wallet' USING ERRCODE='23514'; END IF;
 SELECT balance_after INTO prior FROM wagering.wallet_ledger_entries WHERE wallet_id=NEW.wallet_id AND wallet_version=NEW.wallet_version-1;
 IF NEW.wallet_version=1 THEN prior:=0;
 ELSIF NEW.wallet_version=2 AND w.opening_balance_minor=0 AND prior IS NULL THEN prior:=0;
 END IF;
 IF prior IS NULL OR prior<>NEW.balance_before THEN RAISE EXCEPTION 'broken ledger chain' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_ledger BEFORE INSERT ON wagering.wallet_ledger_entries FOR EACH ROW EXECUTE FUNCTION wagering.guard_ledger();
CREATE FUNCTION wagering.check_wallet_change() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e wagering.wallet_ledger_entries;
BEGIN
 SELECT * INTO e FROM wagering.wallet_ledger_entries WHERE wallet_id=NEW.id AND wallet_version=NEW.version;
 IF TG_OP='INSERT' AND NEW.opening_balance_minor=0 THEN
  IF e.id IS NOT NULL THEN RAISE EXCEPTION 'zero opening cannot have ledger' USING ERRCODE='23514'; END IF;
 ELSE
  IF e.id IS NULL OR e.balance_after<>NEW.balance_minor OR (TG_OP='UPDATE' AND e.balance_before<>OLD.balance_minor) THEN RAISE EXCEPTION 'wallet mutation requires matching ledger' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER wallet_consistency AFTER INSERT OR UPDATE ON wagering.wallets DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION wagering.check_wallet_change();
CREATE FUNCTION wagering.money_json(n bigint,c text) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT jsonb_build_object('amount',(n/100)::text||'.'||lpad((n%100)::text,2,'0'),'currency',c)
$$;
CREATE FUNCTION wagering.guard_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND (NEW.event_id,NEW.transaction_id,NEW.event_type,NEW.aggregate_id,NEW.payload,NEW.occurred_at) IS DISTINCT FROM (OLD.event_id,OLD.transaction_id,OLD.event_type,OLD.aggregate_id,OLD.payload,OLD.occurred_at) THEN
 RAISE EXCEPTION 'immutable event snapshot' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_outbox BEFORE UPDATE ON wagering.outbox FOR EACH ROW EXECUTE FUNCTION wagering.guard_outbox();
CREATE FUNCTION wagering.assert_transaction(target uuid) RETURNS void LANGUAGE plpgsql AS $$
DECLARE t wagering.wager_transactions; e wagering.wallet_ledger_entries; r wagering.wager_transactions; re wagering.wallet_ledger_entries; w wagering.wallets; o wagering.outbox; expected text;
BEGIN
 SELECT * INTO STRICT t FROM wagering.wager_transactions WHERE id=target;
 SELECT * INTO e FROM wagering.wallet_ledger_entries WHERE transaction_id=target;
 IF t.status='PROCESSED' THEN
  SELECT * INTO w FROM wagering.wallets WHERE id=t.wallet_id;
  IF w.id IS NULL OR (w.player_id,w.currency) IS DISTINCT FROM (t.player_id,t.currency) THEN RAISE EXCEPTION 'wallet context mismatch' USING ERRCODE='23514'; END IF;
  IF t.kind='LOSS' THEN
   IF e.id IS NOT NULL THEN RAISE EXCEPTION 'LOSS cannot move money' USING ERRCODE='23514'; END IF;
  ELSE
   IF e.id IS NULL OR (e.wallet_id,e.amount_minor,e.currency,e.balance_after,e.wallet_version,e.created_at) IS DISTINCT FROM (t.wallet_id,t.amount_minor,t.currency,t.result_balance_minor,t.result_version,t.updated_at) THEN RAISE EXCEPTION 'transaction ledger mismatch' USING ERRCODE='23514'; END IF;
   IF (t.kind='BET' AND e.direction<>'DEBIT') OR (t.kind IN ('OPENING','WIN','REFUND') AND e.direction<>'CREDIT') OR (t.kind='OPENING' AND (e.wallet_version<>1 OR e.balance_before<>0)) OR (t.kind<>'OPENING' AND e.wallet_version<2) THEN RAISE EXCEPTION 'invalid movement direction or version' USING ERRCODE='23514'; END IF;
  END IF;
  IF t.reference_external_id<>'' THEN
   SELECT * INTO r FROM wagering.wager_transactions WHERE id=t.resolved_reference_id;
   IF r.id IS NULL OR r.status<>'PROCESSED' OR (r.provider_id,r.external_id,r.player_id,r.wallet_id,r.currency,r.round_id) IS DISTINCT FROM (t.provider_id,t.reference_external_id,t.player_id,t.wallet_id,t.currency,t.round_id)
    OR (t.kind IN ('WIN','REFUND') AND r.kind<>'BET') OR (t.kind='ROLLBACK' AND r.kind NOT IN ('BET','WIN','REFUND')) OR (t.kind IN ('REFUND','ROLLBACK') AND t.amount_minor<>r.amount_minor) THEN RAISE EXCEPTION 'invalid reference' USING ERRCODE='23514'; END IF;
   SELECT * INTO re FROM wagering.wallet_ledger_entries WHERE transaction_id=r.id;
   IF re.id IS NULL OR (t.kind='ROLLBACK' AND e.direction=re.direction) THEN RAISE EXCEPTION 'invalid reversal' USING ERRCODE='23514'; END IF;
  END IF;
 ELSE
  IF e.id IS NOT NULL THEN RAISE EXCEPTION 'nonprocessed transaction has ledger' USING ERRCODE='23514'; END IF;
 END IF;
 IF t.result_version IS NOT NULL THEN
  SELECT * INTO w FROM wagering.wallets WHERE id=t.wallet_id;
  IF w.id IS NULL OR (w.player_id,w.currency) IS DISTINCT FROM (t.player_id,t.currency) OR t.result_version>w.version THEN RAISE EXCEPTION 'invalid result wallet' USING ERRCODE='23514'; END IF;
  IF NOT EXISTS(SELECT 1 FROM wagering.wallet_ledger_entries h WHERE h.wallet_id=t.wallet_id AND h.wallet_version=t.result_version AND h.balance_after=t.result_balance_minor)
   AND NOT (t.result_version=1 AND w.opening_balance_minor=0 AND t.result_balance_minor=0) THEN RAISE EXCEPTION 'result has no matching historical balance' USING ERRCODE='23514'; END IF;
 END IF;
 IF t.status IN ('PENDING','PENDING_REFERENCE') AND NOT EXISTS(SELECT 1 FROM wagering.transaction_work WHERE transaction_id=target) THEN RAISE EXCEPTION 'pending transaction requires durable work' USING ERRCODE='23514'; END IF;
 IF t.status NOT IN ('PENDING','PENDING_REFERENCE') AND EXISTS(SELECT 1 FROM wagering.transaction_work WHERE transaction_id=target) THEN RAISE EXCEPTION 'terminal transaction has pending work' USING ERRCODE='23514'; END IF;
 expected:=CASE t.status WHEN 'PROCESSED' THEN 'WagerTransactionProcessed' WHEN 'REJECTED' THEN 'WagerTransactionRejected' WHEN 'PENDING_REFERENCE' THEN 'WagerTransactionPendingReference' ELSE NULL END;
 IF expected IS NOT NULL AND NOT EXISTS(SELECT 1 FROM wagering.outbox WHERE transaction_id=target AND event_type=expected) THEN RAISE EXCEPTION 'missing transaction outbox event' USING ERRCODE='23514'; END IF;
 IF (t.status='PROCESSED' AND t.kind<>'LOSS') <> EXISTS(SELECT 1 FROM wagering.outbox WHERE transaction_id=target AND event_type='WalletBalanceChanged') THEN RAISE EXCEPTION 'invalid balance outbox event count' USING ERRCODE='23514'; END IF;
 FOR o IN SELECT * FROM wagering.outbox WHERE transaction_id=target LOOP
  IF o.aggregate_id<>t.wallet_id OR (o.payload->>'eventId') IS DISTINCT FROM o.event_id::text OR (o.payload->>'eventType') IS DISTINCT FROM o.event_type OR (o.payload->>'aggregateId') IS DISTINCT FROM t.wallet_id::text OR (o.payload->>'version') IS DISTINCT FROM '1' OR coalesce(o.payload->>'correlationId','')='' OR (o.payload->>'occurredAt')::timestamptz IS DISTINCT FROM o.occurred_at OR (o.payload#>>'{data,transactionId}') IS DISTINCT FROM t.id::text OR (o.payload#>>'{data,walletId}') IS DISTINCT FROM t.wallet_id::text THEN RAISE EXCEPTION 'invalid outbox envelope' USING ERRCODE='23514'; END IF;
  IF o.event_type='WalletBalanceChanged' THEN
   IF (o.payload#>'{data,money}') IS DISTINCT FROM wagering.money_json(e.amount_minor,e.currency) OR (o.payload#>'{data,balanceBefore}') IS DISTINCT FROM wagering.money_json(e.balance_before,e.currency) OR (o.payload#>'{data,balanceAfter}') IS DISTINCT FROM wagering.money_json(e.balance_after,e.currency) OR (o.payload#>>'{data,direction}') IS DISTINCT FROM e.direction OR (o.payload#>>'{data,walletVersion}') IS DISTINCT FROM e.wallet_version::text THEN RAISE EXCEPTION 'incorrect balance event' USING ERRCODE='23514'; END IF;
  ELSE
   IF (o.payload#>>'{data,playerId}') IS DISTINCT FROM t.player_id::text THEN RAISE EXCEPTION 'event player mismatch' USING ERRCODE='23514'; END IF;
   IF t.kind='OPENING' THEN
    IF (o.payload#>>'{data,origin}') IS DISTINCT FROM 'INTERNAL' OR o.payload#>'{data,external}' IS NOT NULL THEN RAISE EXCEPTION 'invalid internal event metadata' USING ERRCODE='23514'; END IF;
   ELSE
    IF (o.payload#>>'{data,origin}') IS DISTINCT FROM 'EXTERNAL' OR (o.payload#>>'{data,external,providerId}') IS DISTINCT FROM t.provider_id OR (o.payload#>>'{data,external,externalTransactionId}') IS DISTINCT FROM t.external_id OR (o.payload#>>'{data,external,payloadHash}') IS DISTINCT FROM t.payload_hash THEN RAISE EXCEPTION 'invalid external event metadata' USING ERRCODE='23514'; END IF;
   END IF;
   IF o.event_type='WagerTransactionRejected' AND (o.payload#>>'{data,failureCode}') IS DISTINCT FROM t.failure_code THEN RAISE EXCEPTION 'event failure mismatch' USING ERRCODE='23514'; END IF;
   IF (o.payload#>>'{data,kind}') IS DISTINCT FROM t.kind OR (o.payload#>'{data,money}') IS DISTINCT FROM wagering.money_json(t.amount_minor,t.currency) THEN RAISE EXCEPTION 'incorrect transaction event' USING ERRCODE='23514'; END IF;
   IF (o.event_type='WagerTransactionProcessed' AND t.status<>'PROCESSED') OR (o.event_type='WagerTransactionRejected' AND t.status<>'REJECTED') OR (o.event_type='WagerTransactionPendingReference' AND NOT t.reference_waited) THEN RAISE EXCEPTION 'event status mismatch' USING ERRCODE='23514'; END IF;
   IF o.event_type IN ('WagerTransactionProcessed','WagerTransactionRejected') THEN
    IF t.result_version IS NULL THEN
     IF o.payload#>'{data,result}' IS NOT NULL THEN RAISE EXCEPTION 'unexpected result' USING ERRCODE='23514'; END IF;
    ELSIF (o.payload#>'{data,result,balance}') IS DISTINCT FROM wagering.money_json(t.result_balance_minor,t.currency) OR (o.payload#>>'{data,result,walletVersion}') IS DISTINCT FROM t.result_version::text THEN RAISE EXCEPTION 'result snapshot mismatch' USING ERRCODE='23514'; END IF;
   END IF;
  END IF;
 END LOOP;
END $$;
CREATE FUNCTION wagering.check_transaction_rows() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target uuid;
BEGIN
 IF TG_TABLE_NAME='wager_transactions' THEN target:=NEW.id;
 ELSIF TG_OP='DELETE' THEN target:=OLD.transaction_id;
 ELSE target:=NEW.transaction_id;
 END IF;
 PERFORM wagering.assert_transaction(target); RETURN NULL;
END $$;
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['wager_transactions','wallet_ledger_entries','outbox','transaction_work','inbox'] LOOP
 EXECUTE format('CREATE CONSTRAINT TRIGGER financial_consistency AFTER INSERT OR UPDATE OR DELETE ON wagering.%I DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION wagering.check_transaction_rows()',t);
 END LOOP;
 FOREACH t IN ARRAY ARRAY['wallets','wager_transactions','outbox'] LOOP
 EXECUTE format('CREATE TRIGGER no_delete BEFORE DELETE ON wagering.%I FOR EACH ROW EXECUTE FUNCTION wagering.deny_mutation()',t);
 EXECUTE format('CREATE TRIGGER no_truncate BEFORE TRUNCATE ON wagering.%I FOR EACH STATEMENT EXECUTE FUNCTION wagering.deny_mutation()',t);
 END LOOP;
END $$;
GRANT SELECT,INSERT ON wagering.wallets,wagering.wager_transactions,wagering.wallet_ledger_entries,wagering.outbox,wagering.inbox,wagering.transaction_work TO wagering_app;
GRANT UPDATE(balance_minor,version,updated_at) ON wagering.wallets TO wagering_app;
GRANT UPDATE(status,failure_code,result_balance_minor,result_version,resolved_reference_id,reference_waited,updated_at) ON wagering.wager_transactions TO wagering_app;
GRANT UPDATE(attempts,next_attempt_at,lease_owner,lease_until,published_at) ON wagering.outbox TO wagering_app;
GRANT UPDATE,DELETE ON wagering.transaction_work TO wagering_app;
