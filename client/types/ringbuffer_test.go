package types

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRingBufferReadWrite(t *testing.T) {
	tests := []struct {
		Name    string
		Cap     int
		ReadCap int
		Insert  []int
		Exp     []int
	}{
		{
			Name:    "less than capacity",
			Cap:     10,
			ReadCap: 5,
			Insert:  []int{1, 2, 3, 4, 5},
			Exp:     []int{1, 2, 3, 4, 5},
		},
		{
			Name:    "more than capacity",
			Cap:     5,
			ReadCap: 5,
			Insert:  []int{1, 2, 3, 4, 5, 6, 7, 8},
			Exp:     []int{4, 5, 6, 7, 8},
		},
		{
			Name:    "equal to capacity",
			Cap:     5,
			ReadCap: 5,
			Insert:  []int{1, 2, 3, 4, 5},
			Exp:     []int{1, 2, 3, 4, 5},
		},
		{
			Name:    "read capacity greater than capacity",
			Cap:     5,
			ReadCap: 10,
			Insert:  []int{1, 2, 3, 4, 5},
			Exp:     []int{1, 2, 3, 4, 5, 0, 0, 0, 0, 0},
		},
		{
			Name:    "read capacity smaller than capacity",
			Cap:     5,
			ReadCap: 3,
			Insert:  []int{1, 2, 3, 4, 5},
			Exp:     []int{1, 2, 3},
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			buf := NewRingBuffer[int](test.Cap)
			for _, item := range test.Insert {
				buf.Insert(item)
			}
			got := make([]int, test.ReadCap)
			read := buf.Read(got)
			if !cmp.Equal(got, test.Exp) {
				t.Errorf("bad read: %v", cmp.Diff(test.Exp, got))
			}
			expReadLen := test.Cap
			if expReadLen > test.ReadCap {
				expReadLen = test.ReadCap
			}
			if got, want := read, expReadLen; got != want {
				t.Error(expReadLen)
				t.Errorf("bad read length: got %d, want %d", got, want)
			}
		})
	}
}

func TestRingBufferDrain(t *testing.T) {
	tests := []struct {
		Name    string
		Cap     int
		ReadCap int
		Insert  []int
		Exp     []int
	}{
		{
			Name:    "less than capacity",
			Cap:     10,
			ReadCap: 5,
			Insert:  []int{1, 2, 3, 4, 5},
			Exp:     []int{1, 2, 3, 4, 5},
		},
		{
			Name:    "more than capacity",
			Cap:     5,
			ReadCap: 5,
			Insert:  []int{1, 2, 3, 4, 5, 6, 7, 8},
			Exp:     []int{4, 5, 6, 7, 8},
		},
		{
			Name:    "equal to capacity",
			Cap:     5,
			ReadCap: 5,
			Insert:  []int{1, 2, 3, 4, 5},
			Exp:     []int{1, 2, 3, 4, 5},
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			buf := NewRingBuffer[int](test.Cap)
			for _, item := range test.Insert {
				buf.Insert(item)
			}
			got := buf.Drain()
			if !cmp.Equal(got, test.Exp) {
				t.Errorf("bad drain: got %v, want %v", got, test.Exp)
			}
		})
	}
}
