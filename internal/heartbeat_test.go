package internal

import (
	"testing"

	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestIsHeartbeatServerToAgent(t *testing.T) {
	uid := []byte("0123456789abcdef")

	tests := []struct {
		name string
		msg  *protobufs.ServerToAgent
		want bool
	}{
		{name: "nil", msg: nil, want: false},
		{name: "empty", msg: &protobufs.ServerToAgent{}, want: false},
		{name: "instance_uid only", msg: &protobufs.ServerToAgent{InstanceUid: uid}, want: true},
		{name: "extra scalar Flags", msg: &protobufs.ServerToAgent{InstanceUid: uid, Flags: 1}, want: false},
		{name: "extra scalar Capabilities", msg: &protobufs.ServerToAgent{InstanceUid: uid, Capabilities: 1}, want: false},
		{
			name: "extra message protobufs.CustomCapabilities",
			msg:  &protobufs.ServerToAgent{InstanceUid: uid, CustomCapabilities: &protobufs.CustomCapabilities{Capabilities: []string{"x"}}},
			want: false,
		},
		{
			name: "content but no instance_uid",
			msg:  &protobufs.ServerToAgent{Flags: 1},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsHeartbeatServerToAgent(tt.msg); got != tt.want {
				t.Fatalf("IsHeartbeatServerToAgent() = %v, want %v", got, tt.want)
			}
		})
	}
}
