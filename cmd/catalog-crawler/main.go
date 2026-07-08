package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"catalog-crawler/internal/sitemap"
	"catalog-crawler/internal/storage"
	"catalog-crawler/internal/structureddata"

	"github.com/gocolly/colly/v2"
	"github.com/gocolly/colly/v2/debug"
	"github.com/spf13/cobra"
)

type resolveOptions struct {
	robotsURL      string
	outputRoot     string
	dbPath         string
	concurrency    int
	minDelayMillis int
	maxDelayMillis int
}

type scrapeOptions struct {
	urls          []string
	inputPath     string
	dbPath        string
	limit         int
	parallelism   int
	delayMillis   int
	randomDelayMS int
	debug         bool
	resume        bool
	skip          int
}

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "catalog-crawler",
		Short: "Catalog crawling utilities",
	}

	rootCmd.AddCommand(newResolveSitemapsCommand())
	rootCmd.AddCommand(newScrapeStructuredDataCommand())

	return rootCmd
}

func newResolveSitemapsCommand() *cobra.Command {
	opts := resolveOptions{
		robotsURL:      sitemap.TargetRobotsURL,
		outputRoot:     "data",
		concurrency:    4,
		minDelayMillis: 0,
		maxDelayMillis: 0,
	}

	cmd := &cobra.Command{
		Use:   "resolve-sitemaps",
		Short: "Resolve sitemap URLs from a robots.txt file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResolveSitemaps(opts)
		},
	}

	cmd.Flags().StringVar(&opts.robotsURL, "robots-url", opts.robotsURL, "robots.txt URL to inspect")
	cmd.Flags().StringVar(&opts.outputRoot, "output", opts.outputRoot, "data directory used for the default per-domain SQLite database")
	cmd.Flags().StringVar(&opts.dbPath, "db", opts.dbPath, "SQLite database path; defaults to data/<host>/scraper.db")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", opts.concurrency, "maximum concurrent sitemap fetches")
	cmd.Flags().IntVar(&opts.minDelayMillis, "min-delay-ms", opts.minDelayMillis, "minimum delay before each sitemap fetch")
	cmd.Flags().IntVar(&opts.maxDelayMillis, "max-delay-ms", opts.maxDelayMillis, "maximum delay before each sitemap fetch")

	return cmd
}

func newScrapeStructuredDataCommand() *cobra.Command {
	opts := scrapeOptions{
		limit:         10,
		parallelism:   2,
		delayMillis:   500,
		randomDelayMS: 500,
		resume:        true,
	}

	cmd := &cobra.Command{
		Use:   "scrape-structured-data",
		Short: "Scrape JSON-LD Product structured data from product pages",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScrapeStructuredData(opts)
		},
	}

	cmd.Flags().StringArrayVar(&opts.urls, "url", opts.urls, "page URL to scrape; may be repeated")
	cmd.Flags().StringVar(&opts.inputPath, "input", opts.inputPath, "legacy pages.json file to seed SQLite")
	cmd.Flags().StringVar(&opts.dbPath, "db", opts.dbPath, "SQLite database path; defaults to data/<host>/scraper.db when URLs are provided")
	cmd.Flags().IntVar(&opts.limit, "limit", opts.limit, "maximum pages to scrape; use 0 for all")
	cmd.Flags().IntVar(&opts.parallelism, "parallelism", opts.parallelism, "maximum concurrent page requests per domain")
	cmd.Flags().IntVar(&opts.delayMillis, "delay-ms", opts.delayMillis, "delay between page requests")
	cmd.Flags().IntVar(&opts.randomDelayMS, "random-delay-ms", opts.randomDelayMS, "additional random delay between page requests")
	cmd.Flags().BoolVar(&opts.debug, "debug", opts.debug, "enable Colly request debug logging")
	cmd.Flags().BoolVar(&opts.resume, "resume", opts.resume, "skip pages already marked visited in SQLite")
	cmd.Flags().IntVar(&opts.skip, "skip", 0, "number of initial pages to skip")
	return cmd
}

func runResolveSitemaps(opts resolveOptions) error {
	ctx := context.Background()
	sitemaps, err := sitemap.FetchRobotsSitemaps(opts.robotsURL)
	if err != nil {
		return err
	}
	if len(sitemaps) == 0 {
		return fmt.Errorf("no sitemaps found in %s", opts.robotsURL)
	}

	config := sitemap.ResolverConfig{
		MaxConcurrency: opts.concurrency,
		MinDelay:       time.Duration(opts.minDelayMillis) * time.Millisecond,
		MaxDelay:       time.Duration(opts.maxDelayMillis) * time.Millisecond,
	}

	report := sitemap.ResolveSitemapsWithConfig(sitemaps, config)
	dbPath := opts.dbPath
	if dbPath == "" {
		dbPath, err = defaultDBPath(opts.outputRoot, opts.robotsURL)
		if err != nil {
			return err
		}
	}

	store, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.SaveSitemapReport(ctx, report); err != nil {
		return err
	}

	fmt.Printf("Found %d sitemap URL(s) in %s\n", len(sitemaps), opts.robotsURL)
	fmt.Printf("Concurrency: %d | jitter: %d-%dms\n", report.MaxConcurrency, report.MinDelayMS, report.MaxDelayMS)
	fmt.Printf("Sitemaps attempted: %d | succeeded: %d | failed: %d\n", report.SitemapsAttempted, report.SitemapsSucceeded, report.SitemapsFailed)
	fmt.Printf("Sitemap indexes: %d | URL sets: %d\n", report.SitemapIndexes, report.URLSets)
	fmt.Printf("Resolved %d final URL(s)\n", len(report.Pages))
	fmt.Printf("Failed pages: %d\n", report.FailedPages)
	fmt.Printf("Sitemap resolution took: %dms\n", report.DurationMS)
	fmt.Printf("Wrote SQLite data to %s\n", dbPath)
	printSlowestSitemaps(report.Sitemaps, 5)
	printFailedSitemaps(report.Sitemaps, 5)

	return nil
}

func runScrapeStructuredData(opts scrapeOptions) error {
	ctx := context.Background()
	started := time.Now()
	pageURLs, err := scrapeInputURLs(opts)
	if err != nil {
		return err
	}
	if opts.parallelism < 1 {
		opts.parallelism = 1
	}
	if opts.delayMillis < 0 {
		opts.delayMillis = 0
	}
	if opts.randomDelayMS < 0 {
		opts.randomDelayMS = 0
	}

	dbPath, err := scrapeDBPath(opts, pageURLs)
	if err != nil {
		return err
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	if len(pageURLs) > 0 {
		if opts.skip > 0 {
			if opts.skip >= len(pageURLs) {
				return fmt.Errorf("skip value %d is greater than or equal to total discovered URLs (%d)", opts.skip, len(pageURLs))
			}
			fmt.Printf("Skipping the first %d URL(s)\n", opts.skip)
			pageURLs = pageURLs[opts.skip:]
		}
		if opts.limit > 0 && len(pageURLs) > opts.limit {
			pageURLs = pageURLs[:opts.limit]
		}
		if err := store.SavePages(ctx, pageURLsToSitemapPages(pageURLs)); err != nil {
			return err
		}
		if opts.resume {
			pageURLs, err = store.PendingFromCandidates(ctx, pageURLs, 0)
			if err != nil {
				return err
			}
		}
	} else {
		pageURLs, err = store.PendingPageURLs(ctx, opts.limit, opts.skip)
		if err != nil {
			return err
		}
		if opts.skip > 0 {
			fmt.Printf("Skipping the first %d URL(s)\n", opts.skip)
		}
	}

	if len(pageURLs) == 0 {
		return fmt.Errorf("no pending page URLs to scrape")
	}

	allowedDomains, err := allowedDomainsFromURLs(pageURLs)
	if err != nil {
		return err
	}

	var failures []scrapeFailure
	pageTimings := make(map[string]time.Time)
	var resultsMu sync.Mutex

	collectorOptions := []colly.CollectorOption{
		colly.Async(true),
		colly.AllowedDomains(allowedDomains...),
		colly.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"),
	}
	if opts.debug {
		collectorOptions = append(collectorOptions, colly.Debugger(&debug.LogDebugger{}))
	}

	collector := colly.NewCollector(collectorOptions...)
	if err := collector.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Parallelism: opts.parallelism,
		Delay:       time.Duration(opts.delayMillis) * time.Millisecond,
		RandomDelay: time.Duration(opts.randomDelayMS) * time.Millisecond,
	}); err != nil {
		return err
	}

	collector.OnHTML(`script[type="application/ld+json"]`, func(element *colly.HTMLElement) {
		var found []structureddata.ProductRecord
		for _, product := range structureddata.ExtractProducts(element.Text) {
			found = append(found, structureddata.ProductRecord{
				URL:  element.Request.URL.String(),
				Data: product,
			})
		}
		if len(found) == 0 {
			return
		}
		for _, product := range found {
			if err := store.InsertStructuredProduct(ctx, product); err != nil {
				resultsMu.Lock()
				failures = append(failures, scrapeFailure{
					URL:   product.URL,
					Error: fmt.Sprintf("store structured product: %v", err),
				})
				resultsMu.Unlock()
			}
		}
	})

	collector.OnRequest(func(request *colly.Request) {
		resultsMu.Lock()
		pageTimings[request.URL.String()] = time.Now()
		resultsMu.Unlock()
	})

	collector.OnResponse(func(response *colly.Response) {
		resultsMu.Lock()
		defer resultsMu.Unlock()

		if err := store.MarkPageSuccess(ctx, response.Request.URL.String(), response.StatusCode, durationSince(pageTimings[response.Request.URL.String()])); err != nil {
			failures = append(failures, scrapeFailure{
				URL:   response.Request.URL.String(),
				Error: fmt.Sprintf("mark page success: %v", err),
			})
		}
	})

	collector.OnError(func(response *colly.Response, err error) {
		resultsMu.Lock()
		defer resultsMu.Unlock()
		if dbErr := store.MarkPageFailure(ctx, response.Request.URL.String(), response.StatusCode, durationSince(pageTimings[response.Request.URL.String()]), err.Error()); dbErr != nil {
			failures = append(failures, scrapeFailure{
				URL:   response.Request.URL.String(),
				Error: fmt.Sprintf("mark page failure: %v", dbErr),
			})
		}
		failures = append(failures, scrapeFailure{
			URL:    response.Request.URL.String(),
			Status: response.StatusCode,
			Error:  err.Error(),
		})
	})

	for _, pageURL := range pageURLs {
		if err := collector.Visit(pageURL); err != nil {
			resultsMu.Lock()
			if dbErr := store.MarkPageFailure(ctx, pageURL, 0, 0, err.Error()); dbErr != nil {
				failures = append(failures, scrapeFailure{
					URL:   pageURL,
					Error: fmt.Sprintf("mark page visit failure: %v", dbErr),
				})
			}
			failures = append(failures, scrapeFailure{
				URL:   pageURL,
				Error: err.Error(),
			})
			resultsMu.Unlock()
		}
	}
	collector.Wait()

	productCount, err := store.Queries.CountStructuredProducts(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("Scraped %d page(s)\n", len(pageURLs))
	fmt.Printf("Total stored Product JSON-LD item(s): %d\n", productCount)
	fmt.Printf("Failures: %d\n", len(failures))
	fmt.Printf("Scrape took: %dms\n", time.Since(started).Milliseconds())
	fmt.Printf("Allowed domains: %v\n", allowedDomains)
	fmt.Printf("SQLite database: %s\n", dbPath)

	return nil
}

type scrapeFailure struct {
	URL    string `json:"url"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error"`
}

func durationSince(started time.Time) int64 {
	if started.IsZero() {
		return 0
	}
	return time.Since(started).Milliseconds()
}

func scrapeInputURLs(opts scrapeOptions) ([]string, error) {
	pageURLs := append([]string(nil), opts.urls...)
	if opts.inputPath == "" {
		return pageURLs, nil
	}

	file, err := os.Open(opts.inputPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var pages []sitemap.PageURL
	if err := json.NewDecoder(file).Decode(&pages); err != nil {
		return nil, err
	}

	for _, page := range pages {
		if page.Loc != "" {
			pageURLs = append(pageURLs, page.Loc)
		}
	}

	return pageURLs, nil
}

func pageURLsToSitemapPages(pageURLs []string) []sitemap.PageURL {
	pages := make([]sitemap.PageURL, 0, len(pageURLs))
	for _, pageURL := range pageURLs {
		pages = append(pages, sitemap.PageURL{Loc: pageURL})
	}
	return pages
}

func scrapeDBPath(opts scrapeOptions, pageURLs []string) (string, error) {
	if opts.dbPath != "" {
		return opts.dbPath, nil
	}
	if len(pageURLs) > 0 {
		return defaultDBPath("data", pageURLs[0])
	}
	return "", fmt.Errorf("--db is required when scraping pending pages without --input or --url")
}

func defaultDBPath(outputRoot string, rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("cannot derive database path from URL %q", rawURL)
	}
	return filepath.Join(outputRoot, host, "scraper.db"), nil
}

func allowedDomainsFromURLs(pageURLs []string) ([]string, error) {
	seen := make(map[string]bool)
	var domains []string

	for _, pageURL := range pageURLs {
		parsed, err := url.Parse(pageURL)
		if err != nil {
			return nil, err
		}
		host := parsed.Hostname()
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		domains = append(domains, host)
	}

	if len(domains) == 0 {
		return nil, fmt.Errorf("no valid domains found in page URLs")
	}

	return domains, nil
}

func printSlowestSitemaps(records []sitemap.SitemapRecord, limit int) {
	if len(records) == 0 || limit <= 0 {
		return
	}

	slowest := append([]sitemap.SitemapRecord(nil), records...)
	sort.Slice(slowest, func(i, j int) bool {
		return slowest[i].DurationMS > slowest[j].DurationMS
	})

	if limit > len(slowest) {
		limit = len(slowest)
	}

	fmt.Println("Slowest sitemaps:")
	for i := 0; i < limit; i++ {
		fmt.Printf("%dms\t%s\n", slowest[i].DurationMS, slowest[i].Loc)
	}
}

func printFailedSitemaps(records []sitemap.SitemapRecord, limit int) {
	if limit <= 0 {
		return
	}

	printed := 0
	for _, record := range records {
		if record.Status != "failed" {
			continue
		}
		if printed == 0 {
			fmt.Println("Failed sitemaps:")
		}
		if printed == limit {
			break
		}
		fmt.Printf("%s\t%s\n", record.Loc, record.Error)
		printed++
	}
}
