package sitehttp

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func TestReadBodyDecodesGzip(t *testing.T) {
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	if _, err := writer.Write([]byte("<html>ok</html>")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := readBody(bytes.NewReader(encoded.Bytes()), "gzip")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "<html>ok</html>" {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestReadBodyAllowsPlainBodyWithGzipHeader(t *testing.T) {
	const body = "<html>already decoded</html>"

	got, err := readBody(bytes.NewReader([]byte(body)), "gzip")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("unexpected body: %q", got)
	}
}
