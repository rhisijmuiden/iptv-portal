package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/rhisijmuiden/iptv-portal/internal/guides"
)

// Guide is a step-by-step setup guide shown on the account pages. The
// Dutch title and text are optional; without them Dutch readers get the
// English ones.
type Guide struct {
	ID        int64
	Builtin   string // key of the default guide it started as; empty for own guides
	Icon      string
	Title     string
	Body      string
	TitleNL   string
	BodyNL    string
	SortOrder int
	Enabled   bool
}

// Localized returns the title and text of g in lang.
func (g Guide) Localized(lang string) (title, body string) {
	title, body = g.Title, g.Body
	if lang == "nl" {
		if strings.TrimSpace(g.TitleNL) != "" {
			title = g.TitleNL
		}
		if strings.TrimSpace(g.BodyNL) != "" {
			body = g.BodyNL
		}
	}
	return title, body
}

const guideSelect = `SELECT id, builtin, icon, title, body, title_nl, body_nl, sort_order, enabled FROM guides`

func scanGuide(sc interface{ Scan(...any) error }) (Guide, error) {
	var g Guide
	err := sc.Scan(&g.ID, &g.Builtin, &g.Icon, &g.Title, &g.Body, &g.TitleNL, &g.BodyNL, &g.SortOrder, &g.Enabled)
	return g, err
}

// Guides returns the guides in their order; onlyEnabled leaves out the
// hidden ones.
func (s *Store) Guides(ctx context.Context, onlyEnabled bool) ([]Guide, error) {
	q := guideSelect
	if onlyEnabled {
		q += ` WHERE enabled = 1`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Guide
	for rows.Next() {
		g, err := scanGuide(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Guide returns one guide.
func (s *Store) Guide(ctx context.Context, id int64) (Guide, error) {
	g, err := scanGuide(s.db.QueryRowContext(ctx, guideSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

// SaveGuide creates (ID 0) or updates a guide and returns its id.
func (s *Store) SaveGuide(ctx context.Context, g Guide) (int64, error) {
	g.Title, g.TitleNL = strings.TrimSpace(g.Title), strings.TrimSpace(g.TitleNL)
	g.Body, g.BodyNL = strings.TrimSpace(g.Body), strings.TrimSpace(g.BodyNL)
	if g.Title == "" {
		return 0, errors.New("title must not be empty")
	}
	if g.Body == "" {
		return 0, errors.New("text must not be empty")
	}
	if len(g.Body)+len(g.BodyNL) > 64<<10 {
		return 0, errors.New("the text is too long")
	}
	if !slices.Contains(guides.Icons, g.Icon) {
		g.Icon = "tv"
	}
	if g.ID == 0 {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO guides(icon, title, body, title_nl, body_nl, sort_order, enabled)
			 VALUES(?, ?, ?, ?, ?, ?, ?)`, g.Icon, g.Title, g.Body, g.TitleNL, g.BodyNL, g.SortOrder, g.Enabled)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE guides SET icon = ?, title = ?, body = ?, title_nl = ?, body_nl = ?, sort_order = ?, enabled = ? WHERE id = ?`,
		g.Icon, g.Title, g.Body, g.TitleNL, g.BodyNL, g.SortOrder, g.Enabled, g.ID)
	if err != nil {
		return 0, err
	}
	return g.ID, mustAffect(res)
}

// DeleteGuide removes a guide.
func (s *Store) DeleteGuide(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM guides WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// RestoreGuides adds the default guides that are no longer there, after
// the others, and returns how many it added.
func (s *Store) RestoreGuides(ctx context.Context) (int, error) {
	var added int
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		added, err = addDefaultGuides(ctx, tx)
		return err
	})
	return added, err
}

func seedGuides(ctx context.Context, tx *sql.Tx) error {
	_, err := addDefaultGuides(ctx, tx)
	return err
}

func addDefaultGuides(ctx context.Context, tx *sql.Tx) (int, error) {
	var last int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), 0) FROM guides`).Scan(&last); err != nil {
		return 0, err
	}
	added := 0
	for _, d := range guides.Defaults {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM guides WHERE builtin = ?)`, d.Key).Scan(&exists); err != nil {
			return added, err
		}
		if exists {
			continue
		}
		last += 10
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO guides(builtin, icon, title, body, title_nl, body_nl, sort_order, enabled) VALUES(?, ?, ?, ?, ?, ?, ?, 1)`,
			d.Key, d.Icon, d.Name, d.Body, d.NameNL, d.BodyNL, last); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}
