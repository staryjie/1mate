-- 0002_add_instance_source.sql
-- Mark instances as "created" (1mate-managed) or "imported" (read-only takeover).
-- Imported instances must NOT have their compose/.env files rewritten on deploy.
ALTER TABLE instances ADD COLUMN source TEXT NOT NULL DEFAULT 'created';
