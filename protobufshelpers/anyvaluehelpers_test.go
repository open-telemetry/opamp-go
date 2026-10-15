package protobufshelpers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/open-telemetry/opamp-go/protobufs"
)

func intValue(v int64) *protobufs.AnyValue {
	return &protobufs.AnyValue{Value: &protobufs.AnyValue_IntValue{IntValue: v}}
}

func stringValue(v string) *protobufs.AnyValue {
	return &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: v}}
}

func arrayValue(values ...*protobufs.AnyValue) *protobufs.AnyValue {
	return &protobufs.AnyValue{
		Value: &protobufs.AnyValue_ArrayValue{
			ArrayValue: &protobufs.ArrayValue{Values: values},
		},
	}
}

func kvlistValue(values ...*protobufs.KeyValue) *protobufs.AnyValue {
	return &protobufs.AnyValue{
		Value: &protobufs.AnyValue_KvlistValue{
			KvlistValue: &protobufs.KeyValueList{Values: values},
		},
	}
}

func keyValue(key string, value *protobufs.AnyValue) *protobufs.KeyValue {
	return &protobufs.KeyValue{Key: key, Value: value}
}

func TestIsEqualAnyValue(t *testing.T) {
	tests := []struct {
		name string
		v1   *protobufs.AnyValue
		v2   *protobufs.AnyValue
		want bool
	}{
		{
			name: "equal strings",
			v1:   stringValue("a"),
			v2:   stringValue("a"),
			want: true,
		},
		{
			name: "different strings",
			v1:   stringValue("a"),
			v2:   stringValue("b"),
			want: false,
		},
		{
			name: "equal arrays",
			v1:   arrayValue(intValue(1), intValue(2)),
			v2:   arrayValue(intValue(1), intValue(2)),
			want: true,
		},
		{
			name: "fully different arrays",
			v1:   arrayValue(intValue(1), intValue(2)),
			v2:   arrayValue(intValue(3), intValue(4)),
			want: false,
		},
		{
			name: "arrays differing after first element",
			v1:   arrayValue(intValue(1), intValue(2)),
			v2:   arrayValue(intValue(1), intValue(9)),
			want: false,
		},
		{
			name: "arrays of different lengths",
			v1:   arrayValue(intValue(1)),
			v2:   arrayValue(intValue(1), intValue(2)),
			want: false,
		},
		{
			name: "equal kvlists",
			v1:   kvlistValue(keyValue("k", intValue(1))),
			v2:   kvlistValue(keyValue("k", intValue(1))),
			want: true,
		},
		{
			name: "kvlists with different values",
			v1:   kvlistValue(keyValue("k", intValue(1))),
			v2:   kvlistValue(keyValue("k", intValue(2))),
			want: false,
		},
		{
			name: "kvlists with different keys",
			v1:   kvlistValue(keyValue("k1", intValue(1))),
			v2:   kvlistValue(keyValue("k2", intValue(1))),
			want: false,
		},
		{
			name: "both nil",
			v1:   nil,
			v2:   nil,
			want: true,
		},
		{
			name: "one nil",
			v1:   intValue(1),
			v2:   nil,
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsEqualAnyValue(tc.v1, tc.v2))
		})
	}
}

func TestIsEqualKeyValue(t *testing.T) {
	tests := []struct {
		name string
		kv1  *protobufs.KeyValue
		kv2  *protobufs.KeyValue
		want bool
	}{
		{
			name: "equal",
			kv1:  keyValue("k", stringValue("v")),
			kv2:  keyValue("k", stringValue("v")),
			want: true,
		},
		{
			name: "different keys",
			kv1:  keyValue("k1", stringValue("v")),
			kv2:  keyValue("k2", stringValue("v")),
			want: false,
		},
		{
			name: "different values",
			kv1:  keyValue("k", stringValue("v1")),
			kv2:  keyValue("k", stringValue("v2")),
			want: false,
		},
		{
			name: "both nil",
			kv1:  nil,
			kv2:  nil,
			want: true,
		},
		{
			name: "one nil",
			kv1:  keyValue("k", stringValue("v")),
			kv2:  nil,
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsEqualKeyValue(tc.kv1, tc.kv2))
		})
	}
}
