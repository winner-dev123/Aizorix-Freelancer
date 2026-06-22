BEGIN;
DROP TABLE IF EXISTS delivery_attempts;
DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS notification_preferences;
DROP TABLE IF EXISTS message_attachments;
DROP TABLE IF EXISTS messages;  -- drops all partitions
DROP TABLE IF EXISTS conversation_participants;
DROP TABLE IF EXISTS conversations;
DROP TABLE IF EXISTS reputation_scores;
DROP TABLE IF EXISTS review_responses;
DROP TABLE IF EXISTS reviews;
COMMIT;
