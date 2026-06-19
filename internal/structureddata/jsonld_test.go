package structureddata

import "testing"

func TestExtractProductsFromObject(t *testing.T) {
	products := ExtractProducts(`{"@context":"https://schema.org","@type":"Product","name":"Kurta"}`)
	if len(products) != 1 {
		t.Fatalf("expected 1 product, got %d", len(products))
	}
	if products[0]["name"] != "Kurta" {
		t.Fatalf("unexpected product name: %v", products[0]["name"])
	}
}

func TestExtractProductsFromGraph(t *testing.T) {
	products := ExtractProducts(`{"@graph":[{"@type":"BreadcrumbList"},{"@type":["Product","Thing"],"name":"Dress"}]}`)
	if len(products) != 1 {
		t.Fatalf("expected 1 product, got %d", len(products))
	}
	if products[0]["name"] != "Dress" {
		t.Fatalf("unexpected product name: %v", products[0]["name"])
	}
}
