package sitemap

import (
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

const TargetRobotsURL = "https://www.zara.com/robots.txt"

// const TargetRobotsURL = "https://www.biba.in/robots.txt"

type ResolverConfig struct {
	MaxConcurrency int
	MinDelay       time.Duration
	MaxDelay       time.Duration
}

type Document struct {
	XMLName  xml.Name       `xml:""`
	Sitemaps []SitemapEntry `xml:"sitemap"`
	URLs     []URLEntry     `xml:"url"`
}

type SitemapEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

type URLEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

type PageURL struct {
	Loc           string `json:"loc"`
	LastMod       string `json:"lastmod,omitempty"`
	SourceSitemap string `json:"source_sitemap"`
}

type FetchFailure struct {
	URL string `json:"url"`
	Err string `json:"err"`
}

type SitemapRecord struct {
	Loc            string `json:"loc"`
	DiscoveredFrom string `json:"discovered_from"`
	Status         string `json:"status"`
	Type           string `json:"type,omitempty"`
	Error          string `json:"error,omitempty"`
	DelayMS        int64  `json:"delay_ms"`
	DurationMS     int64  `json:"duration_ms"`
}

type ResolveReport struct {
	Pages             []PageURL       `json:"-"`
	Sitemaps          []SitemapRecord `json:"-"`
	DurationMS        int64           `json:"duration_ms"`
	MaxConcurrency    int             `json:"max_concurrency"`
	MinDelayMS        int64           `json:"min_delay_ms"`
	MaxDelayMS        int64           `json:"max_delay_ms"`
	SitemapsAttempted int             `json:"sitemaps_attempted"`
	SitemapsSucceeded int             `json:"sitemaps_succeeded"`
	SitemapsFailed    int             `json:"sitemaps_failed"`
	SitemapIndexes    int             `json:"sitemap_indexes"`
	URLSets           int             `json:"url_sets"`
	FinalURLs         int             `json:"final_urls"`
	FailedPages       int             `json:"failed_pages"`
}

type queuedSitemap struct {
	URL            string
	DiscoveredFrom string
}

func DefaultResolverConfig() ResolverConfig {
	return ResolverConfig{
		MaxConcurrency: 4,
		MinDelay:       0 * time.Millisecond,
		MaxDelay:       0 * time.Millisecond,
	}
}

func FetchRobotsSitemaps(url string) ([]string, error) {
	resp, err := get(url, "text/plain,*/*")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch robots.txt: unexpected status %s", resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return ParseRobotsSitemaps(string(data)), nil
}

func ParseRobotsSitemaps(robots string) []string {
	var sitemaps []string

	for _, line := range strings.Split(robots, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "sitemap") {
			continue
		}

		if sitemapURL := strings.TrimSpace(value); sitemapURL != "" {
			sitemaps = append(sitemaps, sitemapURL)
		}
	}

	return sitemaps
}

func ResolveSitemaps(startURLs []string) ResolveReport {
	return ResolveSitemapsWithConfig(startURLs, DefaultResolverConfig())
}

func ResolveSitemapsWithConfig(startURLs []string, config ResolverConfig) ResolveReport {
	started := time.Now()
	config = normalizeResolverConfig(config)

	var report ResolveReport
	seen := make(map[string]bool)
	tasks := make(chan queuedSitemap, config.MaxConcurrency)
	var tasksWG sync.WaitGroup
	var mu sync.Mutex

	report.MaxConcurrency = config.MaxConcurrency
	report.MinDelayMS = config.MinDelay.Milliseconds()
	report.MaxDelayMS = config.MaxDelay.Milliseconds()

	enqueue := func(task queuedSitemap) {
		mu.Lock()
		if seen[task.URL] {
			mu.Unlock()
			return
		}
		seen[task.URL] = true
		tasksWG.Add(1)
		mu.Unlock()

		go func() {
			tasks <- task
		}()
	}

	for workerID := 0; workerID < config.MaxConcurrency; workerID++ {
		rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))
		go func() {
			for current := range tasks {
				record, childSitemaps, pages := resolveOneSitemap(current, config, rng)

				mu.Lock()
				report.SitemapsAttempted++
				report.Sitemaps = append(report.Sitemaps, record)
				if record.Status == "success" {
					report.SitemapsSucceeded++
					switch record.Type {
					case "sitemapindex":
						report.SitemapIndexes++
					case "urlset":
						report.URLSets++
					}
				} else {
					report.SitemapsFailed++
				}
				report.Pages = append(report.Pages, pages...)
				mu.Unlock()

				for _, child := range childSitemaps {
					enqueue(child)
				}

				tasksWG.Done()
			}
		}()
	}

	for _, startURL := range startURLs {
		enqueue(queuedSitemap{
			URL:            startURL,
			DiscoveredFrom: "robots.txt",
		})
	}

	tasksWG.Wait()
	close(tasks)

	report.FinalURLs = len(report.Pages)
	report.DurationMS = time.Since(started).Milliseconds()
	return report
}

func normalizeResolverConfig(config ResolverConfig) ResolverConfig {
	if config.MaxConcurrency < 1 {
		config.MaxConcurrency = 1
	}
	if config.MinDelay < 0 {
		config.MinDelay = 0
	}
	if config.MaxDelay < config.MinDelay {
		config.MaxDelay = config.MinDelay
	}
	return config
}

func resolveOneSitemap(current queuedSitemap, config ResolverConfig, rng *rand.Rand) (SitemapRecord, []queuedSitemap, []PageURL) {
	record := SitemapRecord{
		Loc:            current.URL,
		DiscoveredFrom: current.DiscoveredFrom,
	}

	record.DelayMS = sleepWithJitter(config, rng).Milliseconds()

	sitemapStarted := time.Now()
	doc, err := FetchDocument(current.URL)
	record.DurationMS = time.Since(sitemapStarted).Milliseconds()
	if err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		return record, nil, nil
	}

	switch doc.XMLName.Local {
	case "sitemapindex":
		record.Status = "success"
		record.Type = "sitemapindex"

		var childSitemaps []queuedSitemap
		for _, entry := range doc.Sitemaps {
			if entry.Loc != "" {
				childSitemaps = append(childSitemaps, queuedSitemap{
					URL:            entry.Loc,
					DiscoveredFrom: current.URL,
				})
			}
		}
		return record, childSitemaps, nil

	case "urlset":
		record.Status = "success"
		record.Type = "urlset"

		pages := make([]PageURL, 0, len(doc.URLs))
		for _, entry := range doc.URLs {
			if entry.Loc == "" {
				continue
			}
			pages = append(pages, PageURL{
				Loc:           entry.Loc,
				LastMod:       entry.LastMod,
				SourceSitemap: current.URL,
			})
		}
		return record, nil, pages

	default:
		record.Status = "failed"
		record.Error = fmt.Sprintf("unsupported root element %q", doc.XMLName.Local)
		return record, nil, nil
	}
}

func sleepWithJitter(config ResolverConfig, rng *rand.Rand) time.Duration {
	delay := config.MinDelay
	if config.MaxDelay > config.MinDelay {
		spread := config.MaxDelay - config.MinDelay
		delay += time.Duration(rng.Int63n(int64(spread) + 1))
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	return delay
}

func FetchDocument(url string) (Document, error) {
	resp, err := get(url, "application/xml,text/xml,*/*")
	if err != nil {
		return Document{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Document{}, fmt.Errorf("fetch sitemap %s: unexpected status %s", url, resp.Status)
	}

	body, err := responseBody(resp)
	if err != nil {
		return Document{}, err
	}
	defer body.Close()

	var doc Document
	if err := xml.NewDecoder(body).Decode(&doc); err != nil {
		return Document{}, err
	}

	return doc, nil
}

func get(url string, accept string) (*http.Response, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en-IN,en;q=0.9")

	return client.Do(req)
}

func responseBody(resp *http.Response) (io.ReadCloser, error) {
	if strings.Contains(resp.Header.Get("Content-Type"), "gzip") ||
		strings.HasSuffix(resp.Request.URL.Path, ".gz") {
		return gzip.NewReader(resp.Body)
	}

	return resp.Body, nil
}
