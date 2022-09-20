package internal

import (
	"github.com/stretchr/testify/assert"
	"math/rand"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func response503() *http.Response {
	return &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     map[string][]string{},
	}
}

func TestExtractRetryAfterHeaderDelaySeconds(t *testing.T) {
	// Generate random n > 0 int
	rand.Seed(time.Now().UnixNano())
	retryIntervalSec := rand.Intn(9999)

	// Generate a 503 status code response with Retry-After = n header
	resp := response503()
	resp.Header.Add(retryAfterHTTPHeader, strconv.Itoa(retryIntervalSec))

	expectedDuration := OptionalDuration{
		Defined:  true,
		Duration: time.Second * time.Duration(retryIntervalSec),
	}
	assert.Equal(t, expectedDuration, ExtractRetryAfterHeader(resp))
}

func TestExtractRetryAfterHeader429StatusCode(t *testing.T) {
	// Generate random n > 0 int
	rand.Seed(time.Now().UnixNano())
	retryIntervalSec := rand.Intn(9999)

	// Generate a 429 status code response with Retry-After = n header
	resp := response503()
	resp.StatusCode = http.StatusTooManyRequests
	resp.Header.Add(retryAfterHTTPHeader, strconv.Itoa(retryIntervalSec))

	expectedDuration := OptionalDuration{
		Defined:  true,
		Duration: time.Second * time.Duration(retryIntervalSec),
	}
	assert.Equal(t, expectedDuration, ExtractRetryAfterHeader(resp))
}

func TestExtractRetryAfterHeaderInvalidFormat(t *testing.T) {
	// Generate a random n > 0 int
	now := time.Now()
	//rand.Seed(now.UnixNano())
	retryIntervalSec := rand.Intn(9999)

	// Set a response with Retry-After header = random n > 0 int
	resp := response503()
	ra := now.Add(time.Second * time.Duration(retryIntervalSec)).UTC()

	// Verify non HTTP-date RFC1123 format isn't being parsed
	resp.Header.Set(retryAfterHTTPHeader, ra.Format(time.RFC1123))
	d := ExtractRetryAfterHeader(resp)
	assert.NotNil(t, d)
	assert.Equal(t, false, d.Defined)
	assert.Equal(t, time.Duration(0), d.Duration)
}

func TestExtractRetryAfterHeaderHttpDate(t *testing.T) {
	// Generate a random n > 0 int
	now := time.Now()
	//rand.Seed(now.UnixNano())
	retryIntervalSec := rand.Intn(9999)

	// Set a response with Retry-After header = random n > 0 int
	resp := response503()
	ra := now.Add(time.Second * time.Duration(retryIntervalSec)).UTC()

	// Verify HTTP-date TimeFormat format is being parsed correctly
	resp.Header.Set(retryAfterHTTPHeader, ra.Format(http.TimeFormat))
	d := ExtractRetryAfterHeader(resp)
	assert.NotNil(t, d)
	assert.Equal(t, true, d.Defined)
	assert.Less(t, d.Duration, time.Second*time.Duration(retryIntervalSec))

	// Verify ANSI time format
	resp.Header.Set(retryAfterHTTPHeader, ra.Format(time.ANSIC))
	d = ExtractRetryAfterHeader(resp)
	assert.NotNil(t, d)
	assert.Equal(t, true, d.Defined)
	assert.Less(t, d.Duration, time.Second*time.Duration(retryIntervalSec))

	// Verify RFC850 time format
	resp.Header.Set(retryAfterHTTPHeader, ra.Format(time.RFC850))
	d = ExtractRetryAfterHeader(resp)
	assert.NotNil(t, d)
	assert.Equal(t, true, d.Defined)
	assert.Less(t, d.Duration, time.Second*time.Duration(retryIntervalSec))
}
