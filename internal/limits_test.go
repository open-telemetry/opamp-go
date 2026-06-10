package internal

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errReadFailed = errors.New("read failed")

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errReadFailed
}

func TestSizeLimitErrorError(t *testing.T) {
	tests := []struct {
		name string
		err  *SizeLimitError
		want string
	}{
		{
			name: "no kind",
			err:  &SizeLimitError{Limit: 1},
			want: "message too large: exceeded 1 bytes",
		},
		{
			name: "with kind",
			err:  &SizeLimitError{Kind: "request body", Limit: 2},
			want: "request body too large: exceeded 2 bytes",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.err.Error())
		})
	}
}

func TestResolveMaxMessageSize(t *testing.T) {
	tests := []struct {
		name           string
		maxMessageSize int64
		want           int64
	}{
		{
			name:           "default",
			maxMessageSize: 0,
			want:           DefaultMaxMessageSize,
		},
		{
			name:           "custom limit",
			maxMessageSize: 1,
			want:           1,
		},
		{
			name:           "disabled limit",
			maxMessageSize: -1,
			want:           -1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ResolveMaxMessageSize(tc.maxMessageSize))
		})
	}
}

func TestCheckSizeLimit(t *testing.T) {
	tests := []struct {
		name    string
		size    int64
		limit   int64
		kind    string
		wantErr bool
	}{
		{
			name:  "disabled limit",
			size:  2,
			limit: -1,
			kind:  "message",
		},
		{
			name:  "equal to limit",
			size:  2,
			limit: 2,
			kind:  "message",
		},
		{
			name:  "below limit",
			size:  1,
			limit: 2,
			kind:  "message",
		},
		{
			name:    "above limit",
			size:    3,
			limit:   2,
			kind:    "message",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSizeLimit(tc.size, tc.limit, tc.kind)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)

			var sizeErr *SizeLimitError
			require.ErrorAs(t, err, &sizeErr)
			assert.Equal(t, tc.kind, sizeErr.Kind)
			assert.Equal(t, tc.limit, sizeErr.Limit)
		})
	}
}

func TestReadAllLimited(t *testing.T) {
	t.Run("unlimited", func(t *testing.T) {
		data, err := ReadAllLimited(strings.NewReader("abc"), -1, "message")
		require.NoError(t, err)
		assert.Equal(t, []byte("abc"), data)
	})

	t.Run("within limit", func(t *testing.T) {
		data, err := ReadAllLimited(strings.NewReader("ab"), 2, "message")
		require.NoError(t, err)
		assert.Equal(t, []byte("ab"), data)
	})

	t.Run("max int64 limit", func(t *testing.T) {
		data, err := ReadAllLimited(strings.NewReader("abc"), math.MaxInt64, "message")
		require.NoError(t, err)
		assert.Equal(t, []byte("abc"), data)
	})

	t.Run("read error", func(t *testing.T) {
		data, err := ReadAllLimited(errorReader{}, 2, "message")
		assert.Nil(t, data)
		assert.ErrorIs(t, err, errReadFailed)
	})

	t.Run("exceeds limit", func(t *testing.T) {
		data, err := ReadAllLimited(strings.NewReader("abc"), 2, "message")
		assert.Nil(t, data)
		assert.ErrorContains(t, err, "message too large")
	})
}

func TestCopyDiscardLimited(t *testing.T) {
	t.Run("unlimited", func(t *testing.T) {
		err := CopyDiscardLimited(strings.NewReader("abc"), -1, "message")
		assert.NoError(t, err)
	})

	t.Run("unlimited read error", func(t *testing.T) {
		err := CopyDiscardLimited(errorReader{}, -1, "message")
		assert.ErrorIs(t, err, errReadFailed)
	})

	t.Run("within limit", func(t *testing.T) {
		err := CopyDiscardLimited(strings.NewReader("ab"), 2, "message")
		assert.NoError(t, err)
	})

	t.Run("max int64 limit", func(t *testing.T) {
		r := strings.NewReader("abc")
		err := CopyDiscardLimited(r, math.MaxInt64, "message")
		require.NoError(t, err)
		assert.Zero(t, r.Len())
	})

	t.Run("read error", func(t *testing.T) {
		err := CopyDiscardLimited(errorReader{}, 2, "message")
		assert.ErrorIs(t, err, errReadFailed)
	})

	t.Run("exceeds limit", func(t *testing.T) {
		err := CopyDiscardLimited(strings.NewReader("abc"), 2, "message")
		assert.ErrorContains(t, err, "message too large")
	})
}
