package server

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestFetchOrionData(t *testing.T) {
	c := NewClient("abed")

	res, err := c.fetchOrionData()
	assert.Nil(t, err, "expecting nil error")
	assert.NotNil(t, res, "expecting non-nil result")
	if res != nil {
		assert.NotNil(t, res.Md5Checksum, "expecting correct Md5Checksum")
		assert.NotNil(t, res.S3Path, "expecting correct S3Path")
		assert.NotNil(t, res.Version, "expecting correct version")
	}
}
