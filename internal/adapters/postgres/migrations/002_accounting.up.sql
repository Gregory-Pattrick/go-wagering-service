CREATE TABLE wagering.accounts (
 id text PRIMARY KEY, currency text NOT NULL CHECK(currency IN ('BRL','USD')),
 role text NOT NULL CHECK(role IN ('WALLET_LIABILITY','OPENING_CLEARING','GAME_CLEARING')),
 wallet_id uuid REFERENCES wagering.wallets(id),
 CHECK ((role='WALLET_LIABILITY')=(wallet_id IS NOT NULL))
);
CREATE UNIQUE INDEX one_wallet_account ON wagering.accounts(wallet_id) WHERE wallet_id IS NOT NULL;
CREATE UNIQUE INDEX one_clearing_account ON wagering.accounts(role,currency) WHERE wallet_id IS NULL;
INSERT INTO wagering.accounts(id,currency,role) VALUES
 ('opening:BRL','BRL','OPENING_CLEARING'),('opening:USD','USD','OPENING_CLEARING'),
 ('game:BRL','BRL','GAME_CLEARING'),('game:USD','USD','GAME_CLEARING');
CREATE TABLE wagering.journals (
 transaction_id uuid PRIMARY KEY REFERENCES wagering.wager_transactions(id),
 ledger_id uuid NOT NULL UNIQUE REFERENCES wagering.wallet_ledger_entries(id),
 created_at timestamptz NOT NULL
);
CREATE TABLE wagering.postings (
 transaction_id uuid NOT NULL REFERENCES wagering.journals(transaction_id),
 side text NOT NULL CHECK(side IN ('DEBIT','CREDIT')),
 account_id text NOT NULL REFERENCES wagering.accounts(id),
 amount_minor bigint NOT NULL CHECK(amount_minor>0), currency text NOT NULL CHECK(currency IN ('BRL','USD')),
 PRIMARY KEY(transaction_id,side), UNIQUE(transaction_id,account_id)
);
CREATE INDEX postings_account ON wagering.postings(account_id,transaction_id);
CREATE FUNCTION wagering.assert_journal(target uuid) RETURNS void LANGUAGE plpgsql AS $$
DECLARE e wagering.wallet_ledger_entries; j wagering.journals; t wagering.wager_transactions; p record; wallet_legs integer:=0; clearing_legs integer:=0; expected_role text;
BEGIN
 SELECT * INTO e FROM wagering.wallet_ledger_entries WHERE transaction_id=target;
 SELECT * INTO j FROM wagering.journals WHERE transaction_id=target;
 IF e.id IS NULL THEN
  IF j.transaction_id IS NOT NULL THEN RAISE EXCEPTION 'journal without financial movement' USING ERRCODE='23514'; END IF;
  RETURN;
 END IF;
 IF j.transaction_id IS NULL OR j.ledger_id<>e.id OR j.created_at<>e.created_at THEN RAISE EXCEPTION 'movement requires matching journal' USING ERRCODE='23514'; END IF;
 SELECT * INTO STRICT t FROM wagering.wager_transactions WHERE id=target;
 expected_role:=CASE t.kind WHEN 'OPENING' THEN 'OPENING_CLEARING' ELSE 'GAME_CLEARING' END;
 FOR p IN SELECT x.*,a.role,a.wallet_id,a.currency AS account_currency FROM wagering.postings x JOIN wagering.accounts a ON a.id=x.account_id WHERE x.transaction_id=target LOOP
  IF p.amount_minor<>e.amount_minor OR p.currency<>e.currency OR p.account_currency<>e.currency THEN RAISE EXCEPTION 'posting amount or currency mismatch' USING ERRCODE='23514'; END IF;
  IF p.role='WALLET_LIABILITY' AND p.wallet_id=e.wallet_id AND p.side=e.direction THEN wallet_legs:=wallet_legs+1;
  ELSIF p.role=expected_role AND p.wallet_id IS NULL AND p.side<>e.direction THEN clearing_legs:=clearing_legs+1;
  ELSE RAISE EXCEPTION 'posting uses incorrect account or side' USING ERRCODE='23514'; END IF;
 END LOOP;
 IF wallet_legs<>1 OR clearing_legs<>1 THEN RAISE EXCEPTION 'incomplete or unbalanced journal' USING ERRCODE='23514'; END IF;
END $$;
CREATE FUNCTION wagering.check_journal_rows() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM wagering.assert_journal(NEW.transaction_id); RETURN NULL;
END $$;
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['journals','postings','wallet_ledger_entries'] LOOP
 EXECUTE format('CREATE CONSTRAINT TRIGGER balanced_journal AFTER INSERT ON wagering.%I DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION wagering.check_journal_rows()',t);
 END LOOP;
 FOREACH t IN ARRAY ARRAY['accounts','journals','postings'] LOOP
 EXECUTE format('CREATE TRIGGER immutable_rows BEFORE UPDATE OR DELETE ON wagering.%I FOR EACH ROW EXECUTE FUNCTION wagering.deny_mutation()',t);
 EXECUTE format('CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON wagering.%I FOR EACH STATEMENT EXECUTE FUNCTION wagering.deny_mutation()',t);
 END LOOP;
END $$;
GRANT SELECT,INSERT ON wagering.accounts,wagering.journals,wagering.postings TO wagering_app;
