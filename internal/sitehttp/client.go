package sitehttp

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

const (
	ModeTLS    = "tls"
	ModeStdlib = "stdlib"

	DefaultProfile = "chrome_146"
	UserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
)

type Config struct {
	Mode         string
	Profile      string
	Timeout      time.Duration
	Debug        bool
	ForceHTTP1   bool
	DisableHTTP3 bool
	NoRedirects  bool
}

type Request struct {
	Method  string
	URL     string
	Accept  string
	Body    io.Reader
	Headers map[string]string
}

type Response struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
	URL        string
}

type Client interface {
	Do(ctx context.Context, req Request) (*Response, error)
	CloseIdleConnections()
}

var (
	defaultOnce         sync.Once
	defaultClient       Client
	defaultStdlibOnce   sync.Once
	defaultStdlibClient Client
)

func DefaultClient() Client {
	defaultOnce.Do(func() {
		var err error
		defaultClient, err = New(Config{Mode: ModeTLS})
		if err != nil {
			defaultClient = DefaultStdlibClient()
		}
	})
	return defaultClient
}

func DefaultStdlibClient() Client {
	defaultStdlibOnce.Do(func() {
		defaultStdlibClient, _ = New(Config{Mode: ModeStdlib})
	})
	return defaultStdlibClient
}

func New(config Config) (Client, error) {
	if config.Mode == "" {
		config.Mode = ModeTLS
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}

	switch strings.ToLower(config.Mode) {
	case ModeTLS:
		return newTLSClient(config)
	case ModeStdlib:
		return &stdlibClient{
			client: &http.Client{Timeout: config.Timeout},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported http client mode %q; expected %q or %q", config.Mode, ModeTLS, ModeStdlib)
	}
}

type tlsClient struct {
	client tlsclient.HttpClient
}

func newTLSClient(config Config) (Client, error) {
	profileName := config.Profile
	if profileName == "" {
		profileName = DefaultProfile
	}
	profile, ok := profiles.MappedTLSClients[profileName]
	if !ok {
		return nil, fmt.Errorf("unsupported tls profile %q", profileName)
	}

	jar := tlsclient.NewCookieJar()
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(profile),
		tlsclient.WithCookieJar(jar),
		tlsclient.WithTimeoutSeconds(int(config.Timeout.Seconds())),
	}
	if config.Debug {
		options = append(options, tlsclient.WithDebug())
	}
	if config.ForceHTTP1 {
		options = append(options, tlsclient.WithForceHttp1())
	}
	if config.DisableHTTP3 {
		options = append(options, tlsclient.WithDisableHttp3())
	}
	if config.NoRedirects {
		options = append(options, tlsclient.WithNotFollowRedirects())
	}

	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, err
	}
	return &tlsClient{client: client}, nil
}

func (c *tlsClient) Do(ctx context.Context, request Request) (*Response, error) {
	method := request.Method
	if method == "" {
		method = fhttp.MethodGet
	}

	req, err := fhttp.NewRequestWithContext(ctx, method, request.URL, request.Body)
	if err != nil {
		return nil, err
	}
	req.Header = browserHeaders(request.Accept)
	for key, value := range request.Headers {
		req.Header.Set(key, value)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := readBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, err
	}

	finalURL := request.URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	return &Response{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Header:     http.Header(resp.Header),
		Body:       body,
		URL:        finalURL,
	}, nil
}

func (c *tlsClient) CloseIdleConnections() {
	c.client.CloseIdleConnections()
}

type stdlibClient struct {
	client *http.Client
}

func (c *stdlibClient) Do(ctx context.Context, request Request) (*Response, error) {
	method := request.Method
	if method == "" {
		method = http.MethodGet
	}

	req, err := http.NewRequestWithContext(ctx, method, request.URL, request.Body)
	if err != nil {
		return nil, err
	}
	for key, values := range http.Header(browserHeaders(request.Accept)) {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Del(fhttp.HeaderOrderKey)
	req.Header.Del(fhttp.PHeaderOrderKey)
	for key, value := range request.Headers {
		req.Header.Set(key, value)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := readBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, err
	}

	finalURL := request.URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	return &Response{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Header:     resp.Header.Clone(),
		Body:       body,
		URL:        finalURL,
	}, nil
}

func (c *stdlibClient) CloseIdleConnections() {
	c.client.CloseIdleConnections()
}

func browserHeaders(accept string) fhttp.Header {
	if accept == "" {
		accept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"
	}

	return fhttp.Header{
		"User-Agent":                {UserAgent},
		"Accept":                    {accept},
		"Accept-Language":           {"en-GB,en;q=0.9,en-US;q=0.8"},
		"Accept-Encoding":           {"gzip, deflate, br"},
		"Cache-Control":             {"max-age=0"},
		"Sec-Ch-Ua":                 {`"Chromium";v="146", "Google Chrome";v="146", "Not_A Brand";v="99"`},
		"Sec-Ch-Ua-Mobile":          {"?0"},
		"Sec-Ch-Ua-Platform":        {`"Windows"`},
		"Upgrade-Insecure-Requests": {"1"},
		"Sec-Fetch-Site":            {"none"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-User":            {"?1"},
		"Sec-Fetch-Dest":            {"document"},
		"Priority":                  {"u=0, i"},
		fhttp.HeaderOrderKey: {
			"user-agent",
			"accept",
			"accept-language",
			"accept-encoding",
			"cache-control",
			"sec-ch-ua",
			"sec-ch-ua-mobile",
			"sec-ch-ua-platform",
			"upgrade-insecure-requests",
			"sec-fetch-site",
			"sec-fetch-mode",
			"sec-fetch-user",
			"sec-fetch-dest",
			"priority",
		},
		fhttp.PHeaderOrderKey: {":method", ":authority", ":scheme", ":path"},
	}
}

func readBody(body io.Reader, contentEncoding string) ([]byte, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	return decodeBody(data, contentEncoding)
}

func decodeBody(data []byte, contentEncoding string) ([]byte, error) {
	encodings := strings.Split(strings.ToLower(contentEncoding), ",")
	for i := len(encodings) - 1; i >= 0; i-- {
		encoding := strings.TrimSpace(encodings[i])
		if encoding == "" {
			continue
		}

		var err error
		data, err = decodeBodyOnce(data, encoding)
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

func decodeBodyOnce(data []byte, contentEncoding string) ([]byte, error) {
	switch contentEncoding {
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			if errors.Is(err, gzip.ErrHeader) {
				return data, nil
			}
			return nil, err
		}
		defer reader.Close()
		return io.ReadAll(reader)
	case "br":
		decoded, err := io.ReadAll(brotli.NewReader(bytes.NewReader(data)))
		if err != nil {
			return data, nil
		}
		return decoded, nil
	case "deflate":
		return decodeDeflateBody(data)
	default:
		return data, nil
	}
}

func decodeDeflateBody(data []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err == nil {
		defer reader.Close()
		return io.ReadAll(reader)
	}

	flateReader := flate.NewReader(bytes.NewReader(data))
	defer flateReader.Close()
	decoded, flateErr := io.ReadAll(flateReader)
	if flateErr != nil {
		return data, nil
	}
	return decoded, nil
}
