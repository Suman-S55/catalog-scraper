package sitemap

import "testing"

func TestParseRobotsSitemaps(t *testing.T) {
	robots := `
User-agent: *
Disallow: /cart
Sitemap: https://example.com/sitemap.xml
sitemap: https://example.com/products.xml
`

	got := ParseRobotsSitemaps(robots)
	if len(got) != 2 {
		t.Fatalf("expected 2 sitemaps, got %d", len(got))
	}
	if got[0] != "https://example.com/sitemap.xml" {
		t.Fatalf("unexpected first sitemap: %s", got[0])
	}
	if got[1] != "https://example.com/products.xml" {
		t.Fatalf("unexpected second sitemap: %s", got[1])
	}
}
