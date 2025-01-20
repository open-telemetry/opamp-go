package internal

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
)

const downloadReporterDefaultInterval = time.Second * 10

type downloadReporter struct {
	start         time.Time
	interval      time.Duration
	packageLength float64

	downloaded atomic.Uint64

	done chan struct{}
}

func newDownloadReporter(interval time.Duration, length int) *downloadReporter {
	if interval <= 0 {
		interval = downloadReporterDefaultInterval
	}
	return &downloadReporter{
		start:         time.Now(),
		interval:      interval,
		packageLength: float64(length),
		done:          make(chan struct{}),
	}
}

// Write tracks the number of bytes downloaded. It will never return an error.
func (p *downloadReporter) Write(b []byte) (int, error) {
	n := len(b)
	p.downloaded.Add(uint64(n))
	return n, nil
}

// report periodically updates the package status details and calls the passed upateFn to send the new status.
func (p *downloadReporter) report(ctx context.Context, status *protobufs.PackageStatus, updateFn func(context.Context, bool) error) {
	go func() {
		// Make sure we wait at least 1s before reporting the download rate in bps to avoid a panic
		time.Sleep(time.Second)
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.done:
				return
			case <-ticker.C:
				downloadTime := time.Now().Sub(p.start)
				downloaded := float64(p.downloaded.Load())
				bps := downloaded / float64(downloadTime/time.Second)
				var downloadPercent float64
				if p.packageLength > 0 {
					downloadPercent = downloaded / p.packageLength * 100
				}
				status.DownloadDetails = &protobufs.PackageDownloadDetails{
					// DownloadPercent may be zero if nothing has been downloaded OR there isn't a Content-Length header in the response.
					DownloadPercent: downloadPercent,
					// DownloadBytesPerSecond may be zero if no bytes from the response body have been read yet.
					DownloadBytesPerSecond: bps,
				}
				_ = updateFn(ctx, true)
			}
		}
	}()
}

// stop the downloadReporter report goroutine
func (p *downloadReporter) stop() {
	close(p.done)
}
