CREATE TABLE IF NOT EXISTS sitemaps (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  url TEXT NOT NULL UNIQUE,
  discovered_from TEXT NOT NULL,
  status TEXT NOT NULL,
  type TEXT,
  error TEXT,
  delay_ms INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS pages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  url TEXT NOT NULL UNIQUE,
  source_sitemap TEXT,
  lastmod TEXT,
  scrape_status TEXT NOT NULL DEFAULT 'pending',
  status_code INTEGER,
  duration_ms INTEGER,
  error TEXT,
  visited_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS structured_products (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  page_id INTEGER NOT NULL,
  raw_json TEXT NOT NULL,
  scraped_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_pages_visited_at ON pages(visited_at);
CREATE INDEX IF NOT EXISTS idx_pages_scrape_status ON pages(scrape_status);
CREATE INDEX IF NOT EXISTS idx_structured_products_page_id ON structured_products(page_id);

