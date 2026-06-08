package data

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestPrometheusClusterNameFromDescription(t *testing.T) {
	tests := []struct {
		name        string
		description *protobufs.AgentDescription
		want        string
		wantOK      bool
	}{
		{
			name: "identifying attribute",
			description: &protobufs.AgentDescription{
				IdentifyingAttributes: []*protobufs.KeyValue{
					stringAttribute("prometheus.cluster", "prod"),
				},
			},
			want:   "prod",
			wantOK: true,
		},
		{
			name: "non-identifying attribute",
			description: &protobufs.AgentDescription{
				NonIdentifyingAttributes: []*protobufs.KeyValue{
					stringAttribute("prometheus.cluster", "staging"),
				},
			},
			want:   "staging",
			wantOK: true,
		},
		{
			name: "empty attribute value",
			description: &protobufs.AgentDescription{
				IdentifyingAttributes: []*protobufs.KeyValue{
					stringAttribute("prometheus.cluster", ""),
				},
			},
			wantOK: false,
		},
		{
			name: "missing attribute",
			description: &protobufs.AgentDescription{
				IdentifyingAttributes: []*protobufs.KeyValue{
					stringAttribute("service.name", "otelcol"),
				},
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := prometheusClusterNameFromDescription(tt.description)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCalculatePrometheusShardAssignments(t *testing.T) {
	first := InstanceId{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	second := InstanceId{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}
	third := InstanceId{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3}
	otherCluster := InstanceId{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4}

	assignments := calculatePrometheusShardAssignments(map[InstanceId]string{
		third:        "prod",
		first:        "prod",
		second:       "prod",
		otherCluster: "staging",
	})

	require.Len(t, assignments, 4)
	assert.Equal(t, prometheusShardAssignment{ClusterName: "prod", ShardIndex: 0, ShardCount: 3}, assignments[first])
	assert.Equal(t, prometheusShardAssignment{ClusterName: "prod", ShardIndex: 1, ShardCount: 3}, assignments[second])
	assert.Equal(t, prometheusShardAssignment{ClusterName: "prod", ShardIndex: 2, ShardCount: 3}, assignments[third])
	assert.Equal(t, prometheusShardAssignment{ClusterName: "staging", ShardIndex: 0, ShardCount: 1}, assignments[otherCluster])
}

func TestApplyPrometheusSharding(t *testing.T) {
	const config = `
receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: my-job
          static_configs:
            - targets:
                - app-1:9100
                - app-2:9100
          relabel_configs:
            - source_labels: [job]
              target_label: existing_label
              action: replace
            - source_labels: [__address__]
              modulus: 99
              target_label: __tmp_opamp_shard
              action: hashmod
            - source_labels: [__tmp_opamp_shard]
              regex: "99"
              action: keep
  prometheus/secondary:
    config:
      scrape_configs:
        - job_name: secondary
`

	assignment := &prometheusShardAssignment{
		ClusterName: "prod",
		ShardIndex:  1,
		ShardCount:  3,
	}

	got, changed := applyPrometheusSharding(config, assignment)
	require.True(t, changed)

	again, changedAgain := applyPrometheusSharding(got, assignment)
	assert.False(t, changedAgain)
	assert.Equal(t, got, again)

	primaryRelabels := scrapeRelabelConfigs(t, got, "prometheus", 0)
	require.Len(t, primaryRelabels, 3)
	assert.Equal(t, "existing_label", primaryRelabels[0]["target_label"])
	assertPrometheusShardRelabels(t, primaryRelabels[1:], 3, "1")

	secondaryRelabels := scrapeRelabelConfigs(t, got, "prometheus/secondary", 0)
	require.Len(t, secondaryRelabels, 2)
	assertPrometheusShardRelabels(t, secondaryRelabels, 3, "1")
}

func TestApplyPrometheusShardingLeavesUnrelatedConfigUnchanged(t *testing.T) {
	const config = `
receivers:
  otlp:
    protocols:
      grpc:
`

	got, changed := applyPrometheusSharding(config, &prometheusShardAssignment{
		ClusterName: "prod",
		ShardIndex:  0,
		ShardCount:  2,
	})

	assert.False(t, changed)
	assert.Equal(t, config, got)
}

func TestPrometheusShardingCalculatorUpdatesPeersOnAgentChanges(t *testing.T) {
	const config = `
receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: my-job
`

	agents := NewAgents(NewPrometheusShardingCalculator())

	firstID := InstanceId{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	secondID := InstanceId{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}
	firstConn := &recordingConnection{}
	secondConn := &recordingConnection{}

	first := agents.FindOrCreateAgent(firstID, firstConn)
	first.CustomInstanceConfig = config
	firstResponse := &protobufs.ServerToAgent{}
	first.UpdateStatus(prometheusClusterStatus("prod"), firstResponse)
	agents.AgentsChanged(first, firstResponse)

	second := agents.FindOrCreateAgent(secondID, secondConn)
	second.CustomInstanceConfig = config
	secondResponse := &protobufs.ServerToAgent{}
	second.UpdateStatus(prometheusClusterStatus("prod"), secondResponse)
	agents.AgentsChanged(second, secondResponse)

	require.Len(t, firstConn.messages, 1)
	firstRelabels := scrapeRelabelConfigs(t, remoteConfigBody(firstConn.messages[0].RemoteConfig), "prometheus", 0)
	assertPrometheusShardRelabels(t, firstRelabels, 2, "0")

	require.NotNil(t, secondResponse.RemoteConfig)
	secondRelabels := scrapeRelabelConfigs(t, remoteConfigBody(secondResponse.RemoteConfig), "prometheus", 0)
	assertPrometheusShardRelabels(t, secondRelabels, 2, "1")

	firstResponse = &protobufs.ServerToAgent{}
	first.UpdateStatus(statusWithoutPrometheusCluster(), firstResponse)
	agents.AgentsChanged(first, firstResponse)

	require.NotNil(t, firstResponse.RemoteConfig)
	assert.NotContains(t, remoteConfigBody(firstResponse.RemoteConfig), "__tmp_opamp_shard")

	require.Len(t, secondConn.messages, 1)
	secondRelabels = scrapeRelabelConfigs(t, remoteConfigBody(secondConn.messages[0].RemoteConfig), "prometheus", 0)
	assertPrometheusShardRelabels(t, secondRelabels, 1, "0")
}

func stringAttribute(key, value string) *protobufs.KeyValue {
	return &protobufs.KeyValue{
		Key: key,
		Value: &protobufs.AnyValue{
			Value: &protobufs.AnyValue_StringValue{StringValue: value},
		},
	}
}

func prometheusClusterStatus(clusterName string) *protobufs.AgentToServer {
	return &protobufs.AgentToServer{
		Capabilities: uint64(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig),
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{
				stringAttribute("prometheus.cluster", clusterName),
			},
		},
	}
}

func statusWithoutPrometheusCluster() *protobufs.AgentToServer {
	return &protobufs.AgentToServer{
		SequenceNum: 1,
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{
				stringAttribute("service.name", "otelcol"),
			},
		},
	}
}

func remoteConfigBody(remoteConfig *protobufs.AgentRemoteConfig) string {
	return string(remoteConfig.Config.ConfigMap[""].Body)
}

type recordingConnection struct {
	mux      sync.Mutex
	messages []*protobufs.ServerToAgent
}

func (conn *recordingConnection) Connection() net.Conn {
	return nil
}

func (conn *recordingConnection) Send(_ context.Context, message *protobufs.ServerToAgent) error {
	conn.mux.Lock()
	defer conn.mux.Unlock()

	conn.messages = append(conn.messages, message)
	return nil
}

func (conn *recordingConnection) Disconnect() error {
	return nil
}

func scrapeRelabelConfigs(t *testing.T, config string, receiverName string, scrapeIndex int) []map[string]any {
	t.Helper()

	var decoded map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(config), &decoded))

	receivers, ok := decoded["receivers"].(map[string]any)
	require.True(t, ok)
	receiver, ok := receivers[receiverName].(map[string]any)
	require.True(t, ok)
	receiverConfig, ok := receiver["config"].(map[string]any)
	require.True(t, ok)
	scrapeConfigs, ok := receiverConfig["scrape_configs"].([]any)
	require.True(t, ok)
	require.Greater(t, len(scrapeConfigs), scrapeIndex)
	scrapeConfig, ok := scrapeConfigs[scrapeIndex].(map[string]any)
	require.True(t, ok)
	relabelConfigs, ok := scrapeConfig["relabel_configs"].([]any)
	require.True(t, ok)

	result := make([]map[string]any, 0, len(relabelConfigs))
	for _, relabelConfig := range relabelConfigs {
		relabelConfigMap, ok := relabelConfig.(map[string]any)
		require.True(t, ok)
		result = append(result, relabelConfigMap)
	}
	return result
}

func assertPrometheusShardRelabels(t *testing.T, relabels []map[string]any, shardCount int, shardIndexRegex string) {
	t.Helper()

	require.Len(t, relabels, 2)
	assert.Equal(t, []any{"__address__"}, relabels[0]["source_labels"])
	assert.Equal(t, shardCount, relabels[0]["modulus"])
	assert.Equal(t, "__tmp_opamp_shard", relabels[0]["target_label"])
	assert.Equal(t, "hashmod", relabels[0]["action"])

	assert.Equal(t, []any{"__tmp_opamp_shard"}, relabels[1]["source_labels"])
	assert.Equal(t, shardIndexRegex, relabels[1]["regex"])
	assert.Equal(t, "keep", relabels[1]["action"])
}
