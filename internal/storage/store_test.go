package storage

import (
	"context"
	"testing"

	"catalog-crawler/internal/sitemap"
	"catalog-crawler/internal/structureddata"
)

func TestStorePersistsSitemapPagesAndProducts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir() + "/scraper.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	report := sitemap.ResolveReport{
		Sitemaps: []sitemap.SitemapRecord{
			{
				Loc:            "https://example.com/sitemap.xml",
				DiscoveredFrom: "robots.txt",
				Status:         "success",
				Type:           "urlset",
				DurationMS:     12,
			},
		},
		Pages: []sitemap.PageURL{
			{
				Loc:           "https://example.com/product",
				LastMod:       "2026-01-01",
				SourceSitemap: "https://example.com/sitemap.xml",
			},
		},
	}

	if err := store.SaveSitemapReport(ctx, report); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSitemapReport(ctx, report); err != nil {
		t.Fatal(err)
	}

	pageCount, err := store.Queries.CountPages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pageCount != 1 {
		t.Fatalf("expected 1 page, got %d", pageCount)
	}

	pending, err := store.PendingPageURLs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending page, got %d", len(pending))
	}

	if err := store.MarkPageSuccess(ctx, "https://example.com/product", 200, 25); err != nil {
		t.Fatal(err)
	}

	pending, err = store.PendingPageURLs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected visited page to be excluded from pending results")
	}

	err = store.InsertStructuredProduct(ctx, structureddata.ProductRecord{
		URL: "https://example.com/product",
		Data: map[string]any{
			"@type": "Product",
			"name":  "Dress",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	productCount, err := store.Queries.CountStructuredProducts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if productCount != 1 {
		t.Fatalf("expected 1 structured product, got %d", productCount)
	}
}

func TestStoreMarksPageFailure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir() + "/scraper.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.SavePages(ctx, []sitemap.PageURL{{Loc: "https://example.com/fail"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPageFailure(ctx, "https://example.com/fail", 403, 50, "forbidden"); err != nil {
		t.Fatal(err)
	}

	page, err := store.Queries.GetPageByURL(ctx, "https://example.com/fail")
	if err != nil {
		t.Fatal(err)
	}
	if page.ScrapeStatus != "failed" {
		t.Fatalf("expected failed status, got %q", page.ScrapeStatus)
	}
	if !page.VisitedAt.Valid {
		t.Fatalf("expected failed page to have visited_at")
	}
}
