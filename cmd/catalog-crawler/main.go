package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"catalog-crawler/internal/sitehttp"
	"catalog-crawler/internal/sitemap"
	"catalog-crawler/internal/storage"
	"catalog-crawler/internal/structureddata"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"
)

type httpOptions struct {
	mode         string
	profile      string
	timeout      time.Duration
	debug        bool
	forceHTTP1   bool
	disableHTTP3 bool
	noRedirects  bool
}

type resolveOptions struct {
	robotsURL      string
	outputRoot     string
	dbPath         string
	concurrency    int
	minDelayMillis int
	maxDelayMillis int
	http           httpOptions
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
	http          httpOptions
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
		http:           defaultHTTPOptions(),
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
	addHTTPFlags(cmd, &opts.http)

	return cmd
}

func newScrapeStructuredDataCommand() *cobra.Command {
	opts := scrapeOptions{
		limit:         10,
		parallelism:   2,
		delayMillis:   500,
		randomDelayMS: 500,
		resume:        true,
		http:          defaultHTTPOptions(),
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
	cmd.Flags().BoolVar(&opts.debug, "debug", opts.debug, "print page request debug logging")
	cmd.Flags().BoolVar(&opts.resume, "resume", opts.resume, "skip pages already marked visited in SQLite")
	cmd.Flags().IntVar(&opts.skip, "skip", 0, "number of initial pages to skip")
	addHTTPFlags(cmd, &opts.http)
	return cmd
}

func runResolveSitemaps(opts resolveOptions) error {
	ctx := context.Background()
	httpClient, err := newHTTPClient(opts.http)
	if err != nil {
		return err
	}
	defer httpClient.CloseIdleConnections()

	sitemaps, err := sitemap.FetchRobotsSitemapsWithClient(ctx, httpClient, opts.robotsURL)
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
		Context:        ctx,
		HTTPClient:     httpClient,
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

	httpClient, err := newHTTPClient(opts.http)
	if err != nil {
		return err
	}
	defer httpClient.CloseIdleConnections()

	failures := scrapeStructuredPages(ctx, httpClient, store, pageURLs, allowedDomains, opts)

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

func defaultHTTPOptions() httpOptions {
	return httpOptions{
		mode:    sitehttp.ModeTLS,
		profile: sitehttp.DefaultProfile,
		timeout: 30 * time.Second,
	}
}

func addHTTPFlags(cmd *cobra.Command, opts *httpOptions) {
	cmd.Flags().StringVar(&opts.mode, "http-mode", opts.mode, "HTTP client mode: tls or stdlib")
	cmd.Flags().StringVar(&opts.profile, "tls-profile", opts.profile, "tls-client browser profile to use when --http-mode=tls")
	cmd.Flags().DurationVar(&opts.timeout, "http-timeout", opts.timeout, "maximum duration for a single HTTP request")
	cmd.Flags().BoolVar(&opts.debug, "tls-debug", opts.debug, "enable tls-client debug logging")
	cmd.Flags().BoolVar(&opts.forceHTTP1, "force-http1", opts.forceHTTP1, "force HTTP/1.1 when using tls-client")
	cmd.Flags().BoolVar(&opts.disableHTTP3, "disable-http3", opts.disableHTTP3, "disable HTTP/3 when using tls-client")
	cmd.Flags().BoolVar(&opts.noRedirects, "no-redirects", opts.noRedirects, "disable automatic HTTP redirects")
}

func newHTTPClient(opts httpOptions) (sitehttp.Client, error) {
	return sitehttp.New(sitehttp.Config{
		Mode:         opts.mode,
		Profile:      opts.profile,
		Timeout:      opts.timeout,
		Debug:        opts.debug,
		ForceHTTP1:   opts.forceHTTP1,
		DisableHTTP3: opts.disableHTTP3,
		NoRedirects:  opts.noRedirects,
	})
}

type scrapeFailure struct {
	URL    string `json:"url"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error"`
}

func scrapeStructuredPages(ctx context.Context, client sitehttp.Client, store *storage.Store, pageURLs []string, allowedDomains []string, opts scrapeOptions) []scrapeFailure {
	jobs := make(chan string)
	allowed := make(map[string]bool, len(allowedDomains))
	for _, domain := range allowedDomains {
		allowed[domain] = true
	}

	var failures []scrapeFailure
	var failuresMu sync.Mutex
	var logMu sync.Mutex
	var completed atomic.Int64
	total := len(pageURLs)
	var wg sync.WaitGroup

	addFailure := func(failure scrapeFailure) {
		failuresMu.Lock()
		failures = append(failures, failure)
		failuresMu.Unlock()
	}

	for workerID := 0; workerID < opts.parallelism; workerID++ {
		wg.Add(1)
		rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))
		go func() {
			defer wg.Done()
			for pageURL := range jobs {
				if !isAllowedPageURL(pageURL, allowed) {
					err := fmt.Errorf("URL outside allowed domains")
					if dbErr := store.MarkPageFailure(ctx, pageURL, 0, 0, err.Error()); dbErr != nil {
						addFailure(scrapeFailure{URL: pageURL, Error: fmt.Sprintf("mark page failure: %v", dbErr)})
					}
					addFailure(scrapeFailure{URL: pageURL, Error: err.Error()})
					logScrapeStatus(opts.debug, &logMu, completed.Add(1), total, "skipped", 0, 0, pageURL, err)
					continue
				}

				sleepBeforePageRequest(opts, rng)
				started := time.Now()
				resp, err := client.Do(ctx, sitehttp.Request{URL: pageURL})
				durationMS := durationSince(started)
				if err != nil {
					if dbErr := store.MarkPageFailure(ctx, pageURL, 0, durationMS, err.Error()); dbErr != nil {
						addFailure(scrapeFailure{URL: pageURL, Error: fmt.Sprintf("mark page failure: %v", dbErr)})
					}
					addFailure(scrapeFailure{URL: pageURL, Error: err.Error()})
					logScrapeStatus(opts.debug, &logMu, completed.Add(1), total, "failed", 0, durationMS, pageURL, err)
					continue
				}

				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					err := fmt.Errorf("unexpected status %s", resp.Status)
					if dbErr := store.MarkPageFailure(ctx, pageURL, resp.StatusCode, durationMS, err.Error()); dbErr != nil {
						addFailure(scrapeFailure{URL: pageURL, Error: fmt.Sprintf("mark page failure: %v", dbErr)})
					}
					addFailure(scrapeFailure{URL: pageURL, Status: resp.StatusCode, Error: err.Error()})
					logScrapeStatus(opts.debug, &logMu, completed.Add(1), total, "failed", resp.StatusCode, durationMS, pageURL, err)
					continue
				}

				products, err := productRecordsFromHTML(pageURL, resp.Body)
				if err != nil {
					if dbErr := store.MarkPageFailure(ctx, pageURL, resp.StatusCode, durationMS, err.Error()); dbErr != nil {
						addFailure(scrapeFailure{URL: pageURL, Error: fmt.Sprintf("mark page failure: %v", dbErr)})
					}
					addFailure(scrapeFailure{URL: pageURL, Status: resp.StatusCode, Error: err.Error()})
					logScrapeStatus(opts.debug, &logMu, completed.Add(1), total, "failed", resp.StatusCode, durationMS, pageURL, err)
					continue
				}

				for _, product := range products {
					if err := store.InsertStructuredProduct(ctx, product); err != nil {
						addFailure(scrapeFailure{
							URL:   product.URL,
							Error: fmt.Sprintf("store structured product: %v", err),
						})
					}
				}

				if err := store.MarkPageSuccess(ctx, pageURL, resp.StatusCode, durationMS); err != nil {
					addFailure(scrapeFailure{
						URL:   pageURL,
						Error: fmt.Sprintf("mark page success: %v", err),
					})
					logScrapeStatus(opts.debug, &logMu, completed.Add(1), total, "failed", resp.StatusCode, durationMS, pageURL, err)
					continue
				}
				logScrapeStatus(opts.debug, &logMu, completed.Add(1), total, "scraped", resp.StatusCode, durationMS, pageURL, nil)
			}
		}()
	}

	for _, pageURL := range pageURLs {
		jobs <- pageURL
	}
	close(jobs)
	wg.Wait()

	return failures
}

func logScrapeStatus(enabled bool, mu *sync.Mutex, completed int64, total int, label string, statusCode int, durationMS int64, pageURL string, err error) {
	if !enabled {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	if err != nil {
		fmt.Printf("[%s]\t[%d/%d]\t%d\t%dms\t%s\t%s\n", label, completed, total, statusCode, durationMS, pageURL, err)
		return
	}
	fmt.Printf("[%s]\t[%d/%d]\t%d\t%dms\t%s\n", label, completed, total, statusCode, durationMS, pageURL)
}

func productRecordsFromHTML(pageURL string, body []byte) ([]structureddata.ProductRecord, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	var products []structureddata.ProductRecord
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, selection *goquery.Selection) {
		for _, product := range structureddata.ExtractProducts(selection.Text()) {
			products = append(products, structureddata.ProductRecord{
				URL:  pageURL,
				Data: product,
			})
		}
	})

	return products, nil
}

func isAllowedPageURL(pageURL string, allowedDomains map[string]bool) bool {
	parsed, err := url.Parse(pageURL)
	if err != nil {
		return false
	}
	return allowedDomains[parsed.Hostname()]
}

func sleepBeforePageRequest(opts scrapeOptions, rng *rand.Rand) {
	delay := time.Duration(opts.delayMillis) * time.Millisecond
	if opts.randomDelayMS > 0 {
		delay += time.Duration(rng.Intn(opts.randomDelayMS+1)) * time.Millisecond
	}
	if delay > 0 {
		time.Sleep(delay)
	}
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
