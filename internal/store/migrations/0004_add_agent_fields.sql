-- Add AgentToken and AgentID columns to hosts table for Agent-mode connections.
-- These fields are used when Host.ConnectionType = "agent".
ALTER TABLE hosts ADD COLUMN agent_token TEXT NOT NULL DEFAULT '';
ALTER TABLE hosts ADD COLUMN agent_id TEXT NOT NULL DEFAULT '';
