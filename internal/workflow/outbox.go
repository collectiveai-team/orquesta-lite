package workflow

import (
	"context"
	"encoding/json"
)

type EventSink interface{ Append(json.RawMessage) error }

func (s *Store) FlushOutbox(ctx context.Context, sink EventSink) error {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,payload_json FROM outbox_events WHERE delivered_at IS NULL ORDER BY created_at,event_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pending struct {
		id  string
		raw json.RawMessage
	}
	var events []pending
	for rows.Next() {
		var event pending
		if err := rows.Scan(&event.id, &event.raw); err != nil {
			return err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, event := range events {
		if err := sink.Append(event.raw); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE outbox_events SET delivered_at=? WHERE event_id=?`, stamp(s.now()), event.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListEvents(ctx context.Context, runID string) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json FROM outbox_events WHERE run_id=? ORDER BY created_at,event_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []json.RawMessage
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		events = append(events, json.RawMessage(raw))
	}
	return events, rows.Err()
}
