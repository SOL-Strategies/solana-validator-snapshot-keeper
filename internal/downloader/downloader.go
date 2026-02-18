package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/log"
	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/discovery"
)

func logger() *log.Logger { return log.Default().WithPrefix("downloader") }

// Options configures the download behavior.
type Options struct {
	MinDownloadSpeedBytes int64 // bytes per second
	MinSpeedCheckDelay    time.Duration
	DownloadConnections   int
	DownloadTimeout       time.Duration
}

// Result contains information about a completed download.
type Result struct {
	FilePath     string
	Bytes        int64
	DurationSecs float64
	SpeedBps     int64 // bytes per second
}

// Download downloads a snapshot from the given URL to the destination directory.
// It uses parallel segmented downloads when the server supports Range requests.
// The download starts as a speed test — if speed is below threshold during the
// measurement period, it returns an error so the caller can try the next candidate.
func Download(ctx context.Context, url string, destDir string, filename string, opts Options) (*Result, error) {
	destPath := filepath.Join(destDir, filename)
	tempPath := destPath + ".tmp"

	// First, HEAD to check Content-Length and Accept-Ranges
	headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating HEAD request: %w", err)
	}

	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		return nil, fmt.Errorf("HEAD request: %w", err)
	}
	headResp.Body.Close()

	contentLength := headResp.ContentLength
	supportsRange := headResp.Header.Get("Accept-Ranges") == "bytes" && contentLength > 0
	snapshotType := discovery.SnapshotTypeFull
	if strings.Contains(filename, "incremental") {
		snapshotType = discovery.SnapshotTypeIncremental
	}

	logger().Info(fmt.Sprintf("downloading %s snapshot - %s", snapshotType, formatBytes(contentLength)),
		"url", url,
		"parallel", supportsRange && opts.DownloadConnections > 1,
		"connections", opts.DownloadConnections,
	)

	start := time.Now()
	var totalBytes int64

	if supportsRange && opts.DownloadConnections > 1 {
		totalBytes, err = downloadParallel(ctx, url, tempPath, contentLength, opts)
	} else {
		totalBytes, err = downloadSingle(ctx, url, tempPath, opts)
	}

	if err != nil {
		os.Remove(tempPath)
		return nil, err
	}

	// Atomic rename
	if err := os.Rename(tempPath, destPath); err != nil {
		os.Remove(tempPath)
		return nil, fmt.Errorf("renaming temp file: %w", err)
	}

	duration := time.Since(start)
	speedBps := float64(totalBytes) / duration.Seconds()

	logger().Info(fmt.Sprintf("downloaded %s snapshot - %s in %s at %s/s", snapshotType, formatBytes(totalBytes), duration, formatBytes(int64(speedBps))),
		"url", url,
		"file", filename,
	)

	return &Result{
		FilePath:     destPath,
		Bytes:        totalBytes,
		DurationSecs: duration.Seconds(),
		SpeedBps:     int64(speedBps),
	}, nil
}

func downloadParallel(ctx context.Context, url string, tempPath string, contentLength int64, opts Options) (int64, error) {
	numConns := opts.DownloadConnections
	chunkSize := contentLength / int64(numConns)

	// Create the output file with the full size
	f, err := os.Create(tempPath)
	if err != nil {
		return 0, fmt.Errorf("creating temp file: %w", err)
	}
	if err := f.Truncate(contentLength); err != nil {
		f.Close()
		return 0, fmt.Errorf("truncating file: %w", err)
	}
	f.Close()

	var (
		totalDownloaded atomic.Int64
		downloadErr     error
		errOnce         sync.Once
		wg              sync.WaitGroup
		speedChecked    atomic.Bool
	)

	downloadCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Speed monitoring goroutine
	if opts.MinSpeedCheckDelay > 0 && opts.MinDownloadSpeedBytes > 0 {
		go func() {
			timer := time.NewTimer(opts.MinSpeedCheckDelay)
			defer timer.Stop()
			select {
			case <-timer.C:
				downloaded := totalDownloaded.Load()
				elapsed := opts.MinSpeedCheckDelay.Seconds()
				speedBps := float64(downloaded) / elapsed
				speedChecked.Store(true)
				if speedBps < float64(opts.MinDownloadSpeedBytes) {
					errOnce.Do(func() {
						downloadErr = fmt.Errorf("speed %s/s below minimum %s/s", formatBytes(int64(speedBps)), formatBytes(opts.MinDownloadSpeedBytes))
					})
					cancel()
				} else {
					logger().Info("speed check passed", "speed", fmt.Sprintf("%s/s", formatBytes(int64(speedBps))))
				}
			case <-downloadCtx.Done():
			}
		}()
	}

	// Progress bar goroutine
	go func() {
		bar := progress.New(progress.WithDefaultGradient(), progress.WithWidth(40))
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		start := time.Now()
		for {
			select {
			case <-ticker.C:
				downloaded := totalDownloaded.Load()
				elapsed := time.Since(start).Seconds()
				speedBps := float64(downloaded) / elapsed
				pct := float64(downloaded) / float64(contentLength)
				eta := 0.0
				if speedBps > 0 {
					eta = float64(contentLength-downloaded) / speedBps
				}
				fmt.Fprintf(os.Stderr, "\r  %s %s/s  eta %s  ",
					bar.ViewAs(pct),
					formatBytes(int64(speedBps)),
					time.Duration(eta)*time.Second,
				)
			case <-downloadCtx.Done():
				fmt.Fprint(os.Stderr, "\r\033[2K") // clear the progress bar line
				return
			}
		}
	}()

	// Launch parallel chunk downloads
	for i := 0; i < numConns; i++ {
		rangeStart := int64(i) * chunkSize
		rangeEnd := rangeStart + chunkSize - 1
		if i == numConns-1 {
			rangeEnd = contentLength - 1
		}

		wg.Add(1)
		go func(index int, start, end int64) {
			defer wg.Done()
			if err := downloadChunk(downloadCtx, url, tempPath, start, end, &totalDownloaded); err != nil {
				errOnce.Do(func() {
					downloadErr = fmt.Errorf("chunk %d: %w", index, err)
				})
				cancel()
			}
		}(i, rangeStart, rangeEnd)
	}

	wg.Wait()

	if downloadErr != nil {
		return totalDownloaded.Load(), downloadErr
	}

	return totalDownloaded.Load(), nil
}

func downloadChunk(ctx context.Context, url string, filePath string, rangeStart, rangeEnd int64, totalDownloaded *atomic.Int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rangeStart, rangeEnd))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("expected 206, got %d", resp.StatusCode)
	}

	f, err := os.OpenFile(filePath, os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, 256*1024) // 256KB buffer
	offset := rangeStart

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := f.WriteAt(buf[:n], offset)
			if writeErr != nil {
				return writeErr
			}
			offset += int64(n)
			totalDownloaded.Add(int64(n))
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}

	return nil
}

func downloadSingle(ctx context.Context, url string, tempPath string, opts Options) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("creating GET request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	f, err := os.Create(tempPath)
	if err != nil {
		return 0, fmt.Errorf("creating temp file: %w", err)
	}
	defer f.Close()

	var totalDownloaded atomic.Int64
	start := time.Now()

	// Speed check goroutine for single download
	downloadCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if opts.MinSpeedCheckDelay > 0 && opts.MinDownloadSpeedBytes > 0 {
		go func() {
			timer := time.NewTimer(opts.MinSpeedCheckDelay)
			defer timer.Stop()
			select {
			case <-timer.C:
				downloaded := totalDownloaded.Load()
				elapsed := opts.MinSpeedCheckDelay.Seconds()
				speedBps := float64(downloaded) / elapsed
				if speedBps < float64(opts.MinDownloadSpeedBytes) {
					cancel()
				}
			case <-downloadCtx.Done():
			}
		}()
	}

	buf := make([]byte, 256*1024)
	var total int64

	for {
		select {
		case <-downloadCtx.Done():
			elapsed := time.Since(start).Seconds()
			speedBps := float64(total) / elapsed
			return total, fmt.Errorf("speed %s/s below minimum %s/s", formatBytes(int64(speedBps)), formatBytes(opts.MinDownloadSpeedBytes))
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				return total, writeErr
			}
			total += int64(n)
			totalDownloaded.Add(int64(n))
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return total, readErr
		}
	}

	return total, nil
}

func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
