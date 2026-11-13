-- ID07: a callback finds its delivery by the delivery's id alone (the primary key leads with the caller)
CREATE INDEX delivery_id ON delivery (id);
-- the daily purge (docs/design.md, the data): by expiry and by receipt (the ended bindings have binding_ended)
CREATE INDEX delivery_expires ON delivery (expires_at);
CREATE INDEX callback_received ON callback (received_at) WHERE state <> 'received';
CREATE INDEX bind_session_expires ON bind_session (expires_at);
