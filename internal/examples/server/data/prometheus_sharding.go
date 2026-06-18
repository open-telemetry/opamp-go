package data

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/open-telemetry/opamp-go/protobufs"
)

const (
	prometheusClusterAttribute = "prometheus.cluster"
	prometheusShardTargetLabel = "__tmp_opamp_shard"
)

type prometheusShardAssignment struct {
	ClusterName string
	ShardIndex  int
	ShardCount  int
}

type PrometheusShardingCalculator struct {
	mux         sync.RWMutex
	assignments map[InstanceId]prometheusShardAssignment
}

func NewPrometheusShardingCalculator() *PrometheusShardingCalculator {
	return &PrometheusShardingCalculator{
		assignments: map[InstanceId]prometheusShardAssignment{},
	}
}

func (calculator *PrometheusShardingCalculator) Calculate(agent *Agent, configBody string) string {
	calculator.mux.RLock()
	assignment, ok := calculator.assignments[agent.InstanceId]
	calculator.mux.RUnlock()
	if !ok {
		return configBody
	}

	shardedConfig, changed := applyPrometheusSharding(configBody, &assignment)
	if !changed {
		return configBody
	}
	return shardedConfig
}

func (calculator *PrometheusShardingCalculator) AgentsChanged(
	agents map[InstanceId]*Agent,
) map[InstanceId]bool {
	clustersByAgent := map[InstanceId]string{}
	for instanceID, agent := range agents {
		clusterName, ok := prometheusClusterName(agent)
		if ok {
			clustersByAgent[instanceID] = clusterName
		}
	}

	nextAssignments := calculatePrometheusShardAssignments(clustersByAgent)

	calculator.mux.Lock()
	defer calculator.mux.Unlock()

	changedAgents := map[InstanceId]bool{}
	for instanceID, nextAssignment := range nextAssignments {
		prevAssignment, ok := calculator.assignments[instanceID]
		if !ok || prevAssignment != nextAssignment {
			changedAgents[instanceID] = true
		}
	}
	for instanceID := range calculator.assignments {
		if _, stillAssigned := nextAssignments[instanceID]; stillAssigned {
			continue
		}
		if _, stillConnected := agents[instanceID]; stillConnected {
			changedAgents[instanceID] = true
		}
	}

	calculator.assignments = nextAssignments
	return changedAgents
}

func prometheusClusterName(agent *Agent) (string, bool) {
	if !agent.HasCapability(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig) {
		return "", false
	}
	return prometheusClusterNameFromDescription(agent.AgentDescription())
}

func prometheusClusterNameFromDescription(description *protobufs.AgentDescription) (string, bool) {
	if description == nil {
		return "", false
	}

	for _, attrs := range [][]*protobufs.KeyValue{
		description.IdentifyingAttributes,
		description.NonIdentifyingAttributes,
	} {
		for _, attr := range attrs {
			if attr.GetKey() != prometheusClusterAttribute {
				continue
			}
			value := strings.TrimSpace(attr.GetValue().GetStringValue())
			if value == "" {
				return "", false
			}
			return value, true
		}
	}
	return "", false
}

func calculatePrometheusShardAssignments(clustersByAgent map[InstanceId]string) map[InstanceId]prometheusShardAssignment {
	agentsByCluster := map[string][]InstanceId{}
	for instanceID, clusterName := range clustersByAgent {
		agentsByCluster[clusterName] = append(agentsByCluster[clusterName], instanceID)
	}

	assignments := map[InstanceId]prometheusShardAssignment{}
	for clusterName, instanceIDs := range agentsByCluster {
		sort.Slice(instanceIDs, func(i, j int) bool {
			return bytes.Compare(instanceIDs[i][:], instanceIDs[j][:]) < 0
		})
		for shardIndex, instanceID := range instanceIDs {
			assignments[instanceID] = prometheusShardAssignment{
				ClusterName: clusterName,
				ShardIndex:  shardIndex,
				ShardCount:  len(instanceIDs),
			}
		}
	}
	return assignments
}

func applyPrometheusSharding(config string, assignment *prometheusShardAssignment) (string, bool) {
	if assignment == nil || assignment.ShardCount <= 0 || assignment.ShardIndex < 0 {
		return config, false
	}

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(config), &root); err != nil {
		return config, false
	}
	if len(root.Content) == 0 {
		return config, false
	}

	changed := applyPrometheusShardingToDocument(root.Content[0], assignment)
	if !changed {
		return config, false
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return config, false
	}
	if err := encoder.Close(); err != nil {
		return config, false
	}
	return buf.String(), true
}

func applyPrometheusShardingToDocument(document *yaml.Node, assignment *prometheusShardAssignment) bool {
	receivers, ok := mappingValue(document, "receivers")
	if !ok || receivers.Kind != yaml.MappingNode {
		return false
	}

	changed := false
	for i := 0; i < len(receivers.Content); i += 2 {
		receiverName := receivers.Content[i].Value
		if !isPrometheusReceiverName(receiverName) {
			continue
		}
		if applyPrometheusShardingToReceiver(receivers.Content[i+1], assignment) {
			changed = true
		}
	}
	return changed
}

func isPrometheusReceiverName(name string) bool {
	return name == "prometheus" || strings.HasPrefix(name, "prometheus/")
}

func applyPrometheusShardingToReceiver(receiver *yaml.Node, assignment *prometheusShardAssignment) bool {
	if receiver.Kind != yaml.MappingNode {
		return false
	}

	receiverConfig, ok := mappingValue(receiver, "config")
	if !ok || receiverConfig.Kind != yaml.MappingNode {
		return false
	}

	scrapeConfigs, ok := mappingValue(receiverConfig, "scrape_configs")
	if !ok || scrapeConfigs.Kind != yaml.SequenceNode {
		return false
	}

	changed := false
	for _, scrapeConfig := range scrapeConfigs.Content {
		if applyPrometheusShardingToScrapeConfig(scrapeConfig, assignment) {
			changed = true
		}
	}
	return changed
}

func applyPrometheusShardingToScrapeConfig(scrapeConfig *yaml.Node, assignment *prometheusShardAssignment) bool {
	if scrapeConfig.Kind != yaml.MappingNode {
		return false
	}

	relabelConfigs, ok := mappingValue(scrapeConfig, "relabel_configs")
	if ok && relabelConfigs.Kind != yaml.SequenceNode {
		return false
	}

	if !ok {
		relabelConfigs = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		scrapeConfig.Content = append(
			scrapeConfig.Content,
			stringNode("relabel_configs"),
			relabelConfigs,
		)
	}

	desiredRelabelConfigs := &yaml.Node{
		Kind: yaml.SequenceNode,
		Tag:  "!!seq",
	}
	for _, relabelConfig := range relabelConfigs.Content {
		if isPrometheusShardRelabelConfig(relabelConfig) {
			continue
		}
		desiredRelabelConfigs.Content = append(desiredRelabelConfigs.Content, relabelConfig)
	}
	desiredRelabelConfigs.Content = append(
		desiredRelabelConfigs.Content,
		prometheusHashmodRelabelConfig(assignment),
		prometheusKeepRelabelConfig(assignment),
	)

	if yamlNodesEqual(relabelConfigs, desiredRelabelConfigs) {
		return false
	}
	relabelConfigs.Content = desiredRelabelConfigs.Content
	return true
}

func isPrometheusShardRelabelConfig(relabelConfig *yaml.Node) bool {
	if relabelConfig.Kind != yaml.MappingNode {
		return false
	}

	if targetLabel, ok := mappingValue(relabelConfig, "target_label"); ok &&
		targetLabel.Value == prometheusShardTargetLabel {
		return true
	}

	sourceLabels, ok := mappingValue(relabelConfig, "source_labels")
	if !ok || sourceLabels.Kind != yaml.SequenceNode {
		return false
	}
	for _, sourceLabel := range sourceLabels.Content {
		if sourceLabel.Value == prometheusShardTargetLabel {
			return true
		}
	}
	return false
}

func prometheusHashmodRelabelConfig(assignment *prometheusShardAssignment) *yaml.Node {
	return mappingNode(
		"source_labels", stringSequenceNode("__address__"),
		"modulus", intNode(assignment.ShardCount),
		"target_label", stringNode(prometheusShardTargetLabel),
		"action", stringNode("hashmod"),
	)
}

func prometheusKeepRelabelConfig(assignment *prometheusShardAssignment) *yaml.Node {
	return mappingNode(
		"source_labels", stringSequenceNode(prometheusShardTargetLabel),
		"regex", stringNode(strconv.Itoa(assignment.ShardIndex)),
		"action", stringNode("keep"),
	)
}

func mappingValue(node *yaml.Node, key string) (*yaml.Node, bool) {
	if node.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1], true
		}
	}
	return nil, false
}

func mappingNode(items ...any) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i < len(items); i += 2 {
		node.Content = append(node.Content, stringNode(items[i].(string)), items[i+1].(*yaml.Node))
	}
	return node
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func intNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}
}

func stringSequenceNode(values ...string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, value := range values {
		node.Content = append(node.Content, stringNode(value))
	}
	return node
}

func yamlNodesEqual(a, b *yaml.Node) bool {
	aBytes, aErr := yaml.Marshal(a)
	bBytes, bErr := yaml.Marshal(b)
	return aErr == nil && bErr == nil && bytes.Equal(aBytes, bBytes)
}
