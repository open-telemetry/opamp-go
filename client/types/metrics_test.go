package types

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestMessageAttrs(t *testing.T) {
	tests := []struct {
		Name        string
		Initial     MessageAttrs
		AddAttrs    []MessageAttr
		RemoveAttrs []MessageAttr
		Exp         MessageAttrs
		ExpSlice    []MessageAttr
	}{
		{
			Name: "basic",
			AddAttrs: []MessageAttr{
				TxMessageAttr,
				AgentToServerMessageAttr,
			},
			Exp:      MessageAttrs(TxMessageAttr | AgentToServerMessageAttr),
			ExpSlice: []MessageAttr{TxMessageAttr, AgentToServerMessageAttr},
		},
		{
			Name:     "add to existing",
			Initial:  MessageAttrs(RxMessageAttr | ServerToAgentMessageAttr),
			AddAttrs: []MessageAttr{ErrorMessageAttr},
			Exp:      MessageAttrs(RxMessageAttr | ServerToAgentMessageAttr | ErrorMessageAttr),
			ExpSlice: []MessageAttr{RxMessageAttr, ServerToAgentMessageAttr, ErrorMessageAttr},
		},
		{
			Name:        "remove from existing",
			Initial:     MessageAttrs(RxMessageAttr | ServerToAgentMessageAttr),
			RemoveAttrs: []MessageAttr{RxMessageAttr},
			Exp:         MessageAttrs(ServerToAgentMessageAttr),
			ExpSlice:    []MessageAttr{ServerToAgentMessageAttr},
		},
		{
			Name:        "add and remove",
			Initial:     MessageAttrs(RxMessageAttr | ServerToAgentMessageAttr),
			RemoveAttrs: []MessageAttr{RxMessageAttr, ServerToAgentMessageAttr},
			AddAttrs:    []MessageAttr{TxMessageAttr, AgentToServerMessageAttr},
			Exp:         MessageAttrs(TxMessageAttr | AgentToServerMessageAttr),
			ExpSlice:    []MessageAttr{TxMessageAttr, AgentToServerMessageAttr},
		},
	}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			initial := test.Initial
			for _, attr := range test.AddAttrs {
				initial.Set(attr)
			}
			for _, attr := range test.RemoveAttrs {
				initial.Unset(attr)
			}
			if got, want := initial, test.Exp; got != want {
				t.Errorf("bad MessageAttrs: got %v, want %v", got, want)
			}
			if got, want := test.Exp.Slice(), test.ExpSlice; !cmp.Equal(got, want) {
				t.Errorf("bad []MessageAttr: %s", cmp.Diff(want, got))
			}
		})
	}
}
