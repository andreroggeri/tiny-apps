// Package settings stores what the dashboard configures: which groups the bot
// listens to, which handler each one goes to, and who is allowed to use it.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

// Route maps a WhatsApp group to a handler. An empty Handler means "ignore".
type Route struct {
	GroupJID  string
	GroupName string
	Handler   string
}

// Sender is a person allowed to talk to the bot, identified by phone number
// in international format without "+" (e.g. 5511999998888).
type Sender struct {
	Phone string
	Name  string
}

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS routes (
	group_jid  TEXT PRIMARY KEY,
	group_name TEXT NOT NULL DEFAULT '',
	handler    TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS senders (
	phone TEXT PRIMARY KEY,
	name  TEXT NOT NULL DEFAULT ''
);`

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// HandlerFor returns the handler for a group, or "" if the group isn't routed.
func (s *Store) HandlerFor(ctx context.Context, groupJID string) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT handler FROM routes WHERE group_jid = ?`, groupJID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return h, err
}

func (s *Store) SetRoute(ctx context.Context, r Route) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO routes (group_jid, group_name, handler) VALUES (?, ?, ?)
		ON CONFLICT (group_jid) DO UPDATE SET group_name = excluded.group_name, handler = excluded.handler`,
		r.GroupJID, r.GroupName, r.Handler)
	return err
}

func (s *Store) Routes(ctx context.Context) ([]Route, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_jid, group_name, handler FROM routes ORDER BY group_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Route
	for rows.Next() {
		var r Route
		if err := rows.Scan(&r.GroupJID, &r.GroupName, &r.Handler); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) IsAllowed(ctx context.Context, phone string) (bool, error) {
	if phone == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM senders WHERE phone = ?`, phone).Scan(&n)
	return n > 0, err
}

func (s *Store) AddSender(ctx context.Context, snd Sender) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO senders (phone, name) VALUES (?, ?)
		ON CONFLICT (phone) DO UPDATE SET name = excluded.name`, snd.Phone, snd.Name)
	return err
}

func (s *Store) RemoveSender(ctx context.Context, phone string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM senders WHERE phone = ?`, phone)
	return err
}

func (s *Store) Senders(ctx context.Context) ([]Sender, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT phone, name FROM senders ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sender
	for rows.Next() {
		var snd Sender
		if err := rows.Scan(&snd.Phone, &snd.Name); err != nil {
			return nil, err
		}
		out = append(out, snd)
	}
	return out, rows.Err()
}
