package internal

import (
	"testing"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
)

func Test_calcHashEffectiveConfig(t *testing.T) {
	testCases := []struct {
		desc         string
		inCfg        *protobufs.EffectiveConfig
		expectedHash []byte
	}{
		{
			desc:         "Missing config",
			inCfg:        &protobufs.EffectiveConfig{},
			expectedHash: []byte{},
		},
		{
			desc: "Empty config",
			inCfg: &protobufs.EffectiveConfig{
				ConfigMap: &protobufs.AgentConfigMap{
					ConfigMap: map[string]*protobufs.AgentConfigFile{},
				},
			},
			expectedHash: []byte{},
		},
		{
			desc: "Full config",
			inCfg: &protobufs.EffectiveConfig{
				ConfigMap: &protobufs.AgentConfigMap{
					ConfigMap: map[string]*protobufs.AgentConfigFile{
						"b.yaml": {
							Body:        []byte(`key: value\nkey2: value2`),
							ContentType: "text/yaml",
						},
						"a.json": {
							Body:        []byte(`{"key": "value", "key2": "value2"}`),
							ContentType: "text/json",
						},
					},
				},
			},
			expectedHash: []byte{0x30, 0xea, 0x8, 0xd0, 0x30, 0x61, 0x42, 0x3f, 0x5e, 0x3e, 0x4a, 0x4, 0xe4, 0xb, 0x70, 0x61, 0x0, 0xaa, 0x5, 0xc2, 0x8c, 0x49, 0x97, 0x98, 0xfd, 0x4, 0x64, 0x1a, 0x72, 0x36, 0xfa, 0x27},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			calcHashEffectiveConfig(tc.inCfg)

			assert.Equal(t, tc.expectedHash, tc.inCfg.Hash)
		})
	}
}
