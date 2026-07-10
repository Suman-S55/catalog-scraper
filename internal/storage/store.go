package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	dbschema "catalog-crawler/internal/db"
	db "catalog-crawler/internal/db/sqlc"
	"catalog-crawler/internal/sitemap"
	"catalog-crawler/internal/structureddata"

	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	Queries *db.Queries
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	if _, err := conn.Exec("PRAGMA foreign_keys = ON"); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := conn.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := conn.Exec(dbschema.SQL); err != nil {
		conn.Close()
		return nil, err
	}

	return &Store{
		db:      conn,
		Queries: db.New(conn),
	}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) SaveSitemapReport(ctx context.Context, report sitemap.ResolveReport) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	queries := s.Queries.WithTx(tx)
	for _, record := range report.Sitemaps {
		if err := queries.UpsertSitemap(ctx, db.UpsertSitemapParams{
			Url:            record.Loc,
			DiscoveredFrom: record.DiscoveredFrom,
			Status:         record.Status,
			Type:           nullString(record.Type),
			Error:          nullString(record.Error),
			DelayMs:        record.DelayMS,
			DurationMs:     record.DurationMS,
		}); err != nil {
			return fmt.Errorf("upsert sitemap %s: %w", record.Loc, err)
		}
	}

	if err := savePages(ctx, queries, report.Pages); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) SavePages(ctx context.Context, pages []sitemap.PageURL) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := savePages(ctx, s.Queries.WithTx(tx), pages); err != nil {
		return err
	}

	return tx.Commit()
}

func savePages(ctx context.Context, queries *db.Queries, pages []sitemap.PageURL) error {
	for _, page := range pages {
		if err := queries.UpsertPage(ctx, db.UpsertPageParams{
			Url:           page.Loc,
			SourceSitemap: nullString(page.SourceSitemap),
			Lastmod:       nullString(page.LastMod),
		}); err != nil {
			return fmt.Errorf("upsert page %s: %w", page.Loc, err)
		}
	}
	return nil
}

func (s *Store) PendingPageURLs(ctx context.Context, limit int, offset int) ([]string, error) {
	var pages []db.Page
	var err error
	if limit > 0 {
		pages, err = s.Queries.ListPendingPages(ctx, db.ListPendingPagesParams{
			LimitCount:  int64(limit),
			OffsetCount: int64(offset),
		})
	} else {
		pages, err = s.Queries.ListAllPendingPages(ctx)
		if err == nil && offset > 0 {
			if offset >= len(pages) {
				pages = nil
			} else {
				pages = pages[offset:]
			}
		}
	}
	if err != nil {
		return nil, err
	}

	urls := make([]string, 0, len(pages))
	for _, page := range pages {
		urls = append(urls, page.Url)
	}
	return urls, nil
}

func (s *Store) PendingFromCandidates(ctx context.Context, candidates []string, limit int) ([]string, error) {
	var urls []string
	for _, candidate := range candidates {
		page, err := s.Queries.GetPageByURL(ctx, candidate)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return nil, err
		}
		if page.VisitedAt.Valid {
			continue
		}
		urls = append(urls, candidate)
		if limit > 0 && len(urls) >= limit {
			break
		}
	}
	return urls, nil
}

func (s *Store) MarkPageSuccess(ctx context.Context, url string, statusCode int, durationMS int64) error {
	return s.Queries.MarkPageSuccess(ctx, db.MarkPageSuccessParams{
		Url:        url,
		StatusCode: nullInt64(int64(statusCode)),
		DurationMs: nullInt64(durationMS),
	})
}

func (s *Store) MarkPageFailure(ctx context.Context, url string, statusCode int, durationMS int64, failure string) error {
	return s.Queries.MarkPageFailure(ctx, db.MarkPageFailureParams{
		Url:        url,
		StatusCode: nullInt64(int64(statusCode)),
		DurationMs: nullInt64(durationMS),
		Error:      nullString(failure),
	})
}

func (s *Store) InsertStructuredProduct(ctx context.Context, product structureddata.ProductRecord) error {
	page, err := s.Queries.GetPageByURL(ctx, product.URL)
	if err != nil {
		return err
	}

	raw, err := json.Marshal(product.Data)
	if err != nil {
		return err
	}

	return s.Queries.InsertStructuredProduct(ctx, db.InsertStructuredProductParams{
		PageID:  page.ID,
		RawJson: string(raw),
	})
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func nullInt64(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}
