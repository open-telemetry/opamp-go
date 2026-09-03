package protobufs

import "testing"

func TestIsHeartbeatServerToAgent(t *testing.T) {
	uid := []byte("0123456789abcdef")

	tests := []struct {
		name string
		msg  *ServerToAgent
		want bool
	}{
		{name: "nil", msg: nil, want: false},
		{name: "empty", msg: &ServerToAgent{}, want: false},
		{name: "instance_uid only", msg: &ServerToAgent{InstanceUid: uid}, want: true},
		{name: "extra scalar Flags", msg: &ServerToAgent{InstanceUid: uid, Flags: 1}, want: false},
		{name: "extra scalar Capabilities", msg: &ServerToAgent{InstanceUid: uid, Capabilities: 1}, want: false},
		{
			name: "extra message CustomCapabilities",
			msg:  &ServerToAgent{InstanceUid: uid, CustomCapabilities: &CustomCapabilities{Capabilities: []string{"x"}}},
			want: false,
		},
		{
			name: "content but no instance_uid",
			msg:  &ServerToAgent{Flags: 1},
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
