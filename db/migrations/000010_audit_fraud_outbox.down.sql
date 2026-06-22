BEGIN;
DROP TABLE IF EXISTS processed_events;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS admin_actions;
DROP TABLE IF EXISTS risk_scores;
DROP TABLE IF EXISTS fraud_signals;
DROP TABLE IF EXISTS fraud_cases;
DROP TABLE IF EXISTS audit_logs;  -- drops all partitions
COMMIT;
