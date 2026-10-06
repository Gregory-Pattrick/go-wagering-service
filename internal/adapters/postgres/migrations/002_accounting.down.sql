DROP TRIGGER balanced_journal ON wagering.wallet_ledger_entries;
DROP TABLE wagering.postings,wagering.journals,wagering.accounts;
DROP FUNCTION wagering.check_journal_rows(),wagering.assert_journal(uuid);
