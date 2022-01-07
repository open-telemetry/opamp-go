package internal

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const retryAfterHTTPHeader = "Retry-After"

type OptionalDuration struct {
	Duration time.Duration
	// true if duration field is defined.
	Defined bool
}

// ExtractRetryAfterHeader extracts Retry-After response header if the status
// is 503 or 429. Returns 0 duration if the header is not found or the status
// is different.
func ExtractRetryAfterHeader(resp *http.Response) OptionalDuration {
	if resp.StatusCode == http.StatusServiceUnavailable ||
		resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := strings.TrimSpace(resp.Header.Get(retryAfterHTTPHeader))
		if retryAfter != "" {
			retryIntervalSec, err := strconv.Atoi(retryAfter)
			if err == nil {
				retryInterval := time.Duration(retryIntervalSec) * time.Second
				return OptionalDuration{Defined: true, Duration: retryInterval}
			}
		}
	}
	return OptionalDuration{Defined: false}
}
