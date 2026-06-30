package internal

import (
	"fmt"
	"io"
	"math"
)

const DefaultMaxMessageSize int64 = 64 * 1024 * 1024

type SizeLimitError struct {
	Kind  string
	Limit int64
}

func (e *SizeLimitError) Error() string {
	if e.Kind == "" {
		return fmt.Sprintf("message too large: exceeded %d bytes", e.Limit)
	}
	return fmt.Sprintf("%s too large: exceeded %d bytes", e.Kind, e.Limit)
}

func ResolveMaxMessageSize(maxMessageSize int64) int64 {
	if maxMessageSize == 0 {
		return DefaultMaxMessageSize
	}
	return maxMessageSize
}

func CheckSizeLimit(size int64, limit int64, kind string) error {
	if limit < 0 || size <= limit {
		return nil
	}
	return &SizeLimitError{
		Kind:  kind,
		Limit: limit,
	}
}

func ReadAllLimited(r io.Reader, limit int64, kind string) ([]byte, error) {
	if limit < 0 {
		return io.ReadAll(r)
	}
	if limit == math.MaxInt64 {
		return io.ReadAll(r)
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, &SizeLimitError{
			Kind:  kind,
			Limit: limit,
		}
	}
	return data, nil
}

func CopyDiscardLimited(r io.Reader, limit int64, kind string) error {
	if limit < 0 {
		_, err := io.Copy(io.Discard, r)
		return err
	}
	if limit == math.MaxInt64 {
		_, err := io.Copy(io.Discard, r)
		return err
	}
	n, err := io.Copy(io.Discard, io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return &SizeLimitError{
			Kind:  kind,
			Limit: limit,
		}
	}
	return nil
}
