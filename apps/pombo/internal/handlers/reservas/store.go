package reservas

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// request is a complete booking request: the chair from start to end on day.
type request struct {
	ID     int64 // set once saved
	Chat   string
	Sender string
	Day    time.Time // midnight, site time zone
	Start  int
	End    int
}

// Store keeps requests for dates that aren't open yet, so a restart before
// the date opens doesn't lose them.
type Store struct {
	db  *sql.DB
	loc *time.Location
}

func OpenStore(ctx context.Context, path string, loc *time.Location) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS pending (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			chat       TEXT NOT NULL,
			sender     TEXT NOT NULL,
			day        TEXT NOT NULL, -- YYYY-MM-DD
			start_hour INTEGER NOT NULL,
			end_hour   INTEGER NOT NULL
		)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db, loc: loc}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Add(ctx context.Context, r request) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO pending (chat, sender, day, start_hour, end_hour) VALUES (?, ?, ?, ?, ?)`,
		r.Chat, r.Sender, r.Day.Format(time.DateOnly), r.Start, r.End)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pending WHERE id = ?`, id)
	return err
}

// List returns pending requests, soonest first. chat "" lists all chats.
func (s *Store) List(ctx context.Context, chat string) ([]request, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, chat, sender, day, start_hour, end_hour FROM pending
		WHERE ? = '' OR chat = ? ORDER BY day, start_hour`, chat, chat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []request
	for rows.Next() {
		var r request
		var day string
		if err := rows.Scan(&r.ID, &r.Chat, &r.Sender, &day, &r.Start, &r.End); err != nil {
			return nil, err
		}
		if r.Day, err = time.ParseInLocation(time.DateOnly, day, s.loc); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
