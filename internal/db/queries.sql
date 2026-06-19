-- name: UpsertSitemap :exec
INSERT INTO sitemaps (
  url,
  discovered_from,
  status,
  type,
  error,
  delay_ms,
  duration_ms
) VALUES (
  sqlc.arg(url),
  sqlc.arg(discovered_from),
  sqlc.arg(status),
  sqlc.narg(type),
  sqlc.narg(error),
  sqlc.arg(delay_ms),
  sqlc.arg(duration_ms)
)
ON CONFLICT(url) DO UPDATE SET
  discovered_from = excluded.discovered_from,
  status = excluded.status,
  type = excluded.type,
  error = excluded.error,
  delay_ms = excluded.delay_ms,
  duration_ms = excluded.duration_ms,
  updated_at = CURRENT_TIMESTAMP;

-- name: UpsertPage :exec
INSERT INTO pages (
  url,
  source_sitemap,
  lastmod
) VALUES (
  sqlc.arg(url),
  sqlc.narg(source_sitemap),
  sqlc.narg(lastmod)
)
ON CONFLICT(url) DO UPDATE SET
  source_sitemap = excluded.source_sitemap,
  lastmod = COALESCE(excluded.lastmod, pages.lastmod),
  updated_at = CURRENT_TIMESTAMP;

-- name: ListPendingPages :many
SELECT id, url, source_sitemap, lastmod, scrape_status, status_code, duration_ms, error, visited_at, created_at, updated_at
FROM pages
WHERE visited_at IS NULL
ORDER BY id
LIMIT sqlc.arg(limit_count);

-- name: ListAllPendingPages :many
SELECT id, url, source_sitemap, lastmod, scrape_status, status_code, duration_ms, error, visited_at, created_at, updated_at
FROM pages
WHERE visited_at IS NULL
ORDER BY id;

-- name: GetPageByURL :one
SELECT id, url, source_sitemap, lastmod, scrape_status, status_code, duration_ms, error, visited_at, created_at, updated_at
FROM pages
WHERE url = sqlc.arg(url);

-- name: MarkPageSuccess :exec
UPDATE pages
SET scrape_status = 'success',
  status_code = sqlc.arg(status_code),
  duration_ms = sqlc.arg(duration_ms),
  error = NULL,
  visited_at = CURRENT_TIMESTAMP,
  updated_at = CURRENT_TIMESTAMP
WHERE url = sqlc.arg(url);

-- name: MarkPageFailure :exec
UPDATE pages
SET scrape_status = 'failed',
  status_code = sqlc.narg(status_code),
  duration_ms = sqlc.narg(duration_ms),
  error = sqlc.arg(error),
  visited_at = CURRENT_TIMESTAMP,
  updated_at = CURRENT_TIMESTAMP
WHERE url = sqlc.arg(url);

-- name: InsertStructuredProduct :exec
INSERT INTO structured_products (
  page_id,
  raw_json
) VALUES (
  sqlc.arg(page_id),
  sqlc.arg(raw_json)
);

-- name: CountPages :one
SELECT COUNT(*) FROM pages;

-- name: CountPendingPages :one
SELECT COUNT(*) FROM pages WHERE visited_at IS NULL;

-- name: CountStructuredProducts :one
SELECT COUNT(*) FROM structured_products;
