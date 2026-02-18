package downloader

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newRangeServer(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			return
		}

		rangeHeader := r.Header.Get("Range")
		if rangeHeader != "" {
			// Parse "bytes=start-end"
			rangeHeader = strings.TrimPrefix(rangeHeader, "bytes=")
			parts := strings.Split(rangeHeader, "-")
			start, _ := strconv.ParseInt(parts[0], 10, 64)
			end, _ := strconv.ParseInt(parts[1], 10, 64)
			if end >= int64(len(data)) {
				end = int64(len(data)) - 1
			}

			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(data[start : end+1])
			return
		}

		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
}

func newSimpleServer(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			// No Accept-Ranges header
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
}

func TestDownload_ParallelWithRange(t *testing.T) {
	// Generate 1MB of random data
	data := make([]byte, 1024*1024)
	rand.Read(data)

	server := newRangeServer(t, data)
	defer server.Close()

	destDir := t.TempDir()
	opts := Options{
		MinDownloadSpeedBytes: 0, // disable speed check for test
		MinSpeedCheckDelay: 0,
		DownloadConnections: 4,
		DownloadTimeout: time.Minute,
	}

	result, err := Download(context.Background(), server.URL+"/snapshot.tar.zst", destDir, "snapshot-100-Hash.tar.zst", opts)
	if err != nil {
		t.Fatal(err)
	}

	if result.Bytes != int64(len(data)) {
		t.Errorf("expected %d bytes, got %d", len(data), result.Bytes)
	}

	// Verify file contents match
	downloaded, err := os.ReadFile(result.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(downloaded) != len(data) {
		t.Fatalf("file size mismatch: %d vs %d", len(downloaded), len(data))
	}
	for i := range data {
		if downloaded[i] != data[i] {
			t.Fatalf("byte mismatch at position %d", i)
			break
		}
	}
}

func TestDownload_SingleConnection(t *testing.T) {
	data := make([]byte, 512*1024)
	rand.Read(data)

	server := newSimpleServer(t, data)
	defer server.Close()

	destDir := t.TempDir()
	opts := Options{
		MinDownloadSpeedBytes: 0,
		MinSpeedCheckDelay: 0,
		DownloadConnections: 4, // should fall back to single since no Range support
		DownloadTimeout: time.Minute,
	}

	result, err := Download(context.Background(), server.URL+"/snapshot.tar.zst", destDir, "snapshot-100-Hash.tar.zst", opts)
	if err != nil {
		t.Fatal(err)
	}

	if result.Bytes != int64(len(data)) {
		t.Errorf("expected %d bytes, got %d", len(data), result.Bytes)
	}
}

func TestDownload_ContextCancellation(t *testing.T) {
	// Server that streams slowly
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "10485760") // 10MB
			w.WriteHeader(http.StatusOK)
			return
		}
		// Write data slowly
		w.WriteHeader(http.StatusOK)
		buf := make([]byte, 1024)
		for {
			_, err := w.Write(buf)
			if err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()

	destDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay
	go func() {
		<-ctx.Done()
	}()
	cancel() // immediate cancel

	opts := Options{
		MinDownloadSpeedBytes: 0,
		MinSpeedCheckDelay: 0,
		DownloadConnections: 1,
		DownloadTimeout: time.Minute,
	}

	_, err := Download(ctx, server.URL+"/snapshot.tar.zst", destDir, "test.tar.zst", opts)
	if err == nil {
		t.Error("expected error on cancelled context")
	}

	// Verify temp file was cleaned up
	tempPath := filepath.Join(destDir, "test.tar.zst.tmp")
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Error("expected temp file to be cleaned up")
	}
}

func TestDownload_AtomicRename(t *testing.T) {
	data := []byte("snapshot data")
	server := newSimpleServer(t, data)
	defer server.Close()

	destDir := t.TempDir()
	opts := Options{
		MinDownloadSpeedBytes: 0,
		MinSpeedCheckDelay: 0,
		DownloadConnections: 1,
		DownloadTimeout: time.Minute,
	}

	result, err := Download(context.Background(), server.URL+"/snapshot.tar.zst", destDir, "snapshot-100-Hash.tar.zst", opts)
	if err != nil {
		t.Fatal(err)
	}

	// Final file should exist
	if _, err := os.Stat(result.FilePath); err != nil {
		t.Errorf("final file not found: %v", err)
	}

	// Temp file should not exist
	tempPath := result.FilePath + ".tmp"
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Error("temp file should not exist after completion")
	}
}
