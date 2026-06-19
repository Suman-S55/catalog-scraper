package structureddata

import (
	"encoding/json"
	"strings"
)

type ProductRecord struct {
	URL  string         `json:"url"`
	Data map[string]any `json:"data"`
}

func ExtractProducts(script string) []map[string]any {
	var value any
	decoder := json.NewDecoder(strings.NewReader(script))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil
	}

	var products []map[string]any
	walkJSONLD(value, &products)
	return products
}

func walkJSONLD(value any, products *[]map[string]any) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			walkJSONLD(item, products)
		}
	case map[string]any:
		if graph, ok := typed["@graph"]; ok {
			walkJSONLD(graph, products)
		}
		if isProductType(typed["@type"]) {
			*products = append(*products, typed)
		}
	}
}

func isProductType(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.EqualFold(typed, "Product") || strings.EqualFold(typed, "https://schema.org/Product")
	case []any:
		for _, item := range typed {
			if isProductType(item) {
				return true
			}
		}
	}

	return false
}
