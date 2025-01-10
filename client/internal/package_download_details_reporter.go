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
		timer := time.NewTimer(p.interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.done:
				return
			case <-timer.C:
				downloadTime := time.Now().Sub(p.start)
				downloaded := p.downloaded.Load()
				bps := downloaded / uint64(downloadTime/time.Second)
				var downloadPercent float64
				if p.packageLength > 0 {
					downloadPercent = float64(downloaded) / float64(p.packageLength) * 100
				}
				status.DownloadDetails = &protobufs.PackageDownloadDetails{
					DownloadPercent:        downloadPercent,
					DownloadBytesPerSecond: bps,
				}
				_ = updateFn(ctx, true)
				timer.Reset(p.interval)
			}
		}
	}()
}

// stop the downloadReporter report goroutine
func (p *downloadReporter) stop() {
	close(p.done)
}
