-- The purge deletes delivered events by delivery date: index only those.
CREATE INDEX outbox_events_delivered ON outbox_events (delivered_at)
    WHERE delivered_at IS NOT NULL;
