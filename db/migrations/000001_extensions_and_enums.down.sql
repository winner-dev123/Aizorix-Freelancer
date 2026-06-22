BEGIN;
DROP FUNCTION IF EXISTS ensure_month_partition(regclass, date);
DROP FUNCTION IF EXISTS set_updated_at();

DROP TYPE IF EXISTS notification_channel;
DROP TYPE IF EXISTS fraud_case_status;
DROP TYPE IF EXISTS dispute_status;
DROP TYPE IF EXISTS withdrawal_status;
DROP TYPE IF EXISTS txn_type;
DROP TYPE IF EXISTS payment_status;
DROP TYPE IF EXISTS escrow_status;
DROP TYPE IF EXISTS screenshot_status;
DROP TYPE IF EXISTS worksession_status;
DROP TYPE IF EXISTS milestone_status;
DROP TYPE IF EXISTS contract_status;
DROP TYPE IF EXISTS proposal_status;
DROP TYPE IF EXISTS experience_level;
DROP TYPE IF EXISTS budget_type;
DROP TYPE IF EXISTS project_status;
DROP TYPE IF EXISTS mfa_type;
DROP TYPE IF EXISTS kyc_status;
DROP TYPE IF EXISTS account_type;
DROP TYPE IF EXISTS user_status;

-- Extensions left in place (other databases may depend on them).
COMMIT;
