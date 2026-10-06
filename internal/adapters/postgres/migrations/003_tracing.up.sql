-- Transport metadata only; no change to financial payloads or idempotency hashes.
CREATE TABLE wagering.outbox_trace_context (
 event_id uuid PRIMARY KEY REFERENCES wagering.outbox(event_id),
 traceparent text NOT NULL CHECK(traceparent ~ '^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$'),
 tracestate text NOT NULL DEFAULT '' CHECK(length(tracestate)<=512)
);
CREATE TRIGGER immutable_rows BEFORE UPDATE OR DELETE ON wagering.outbox_trace_context
 FOR EACH ROW EXECUTE FUNCTION wagering.deny_mutation();
CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON wagering.outbox_trace_context
 FOR EACH STATEMENT EXECUTE FUNCTION wagering.deny_mutation();
GRANT SELECT,INSERT ON wagering.outbox_trace_context TO wagering_app;
