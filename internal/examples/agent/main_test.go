package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddExtraAttribute(t *testing.T) {
	attrs := map[string]string{}

	require.NoError(t, addExtraAttribute(attrs, " prometheus.cluster = demo "))

	assert.Equal(t, map[string]string{"prometheus.cluster": "demo"}, attrs)
}

func TestAddExtraAttributeRejectsInvalidFormat(t *testing.T) {
	err := addExtraAttribute(map[string]string{}, "prometheus.cluster")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "key=value")
}

func TestLoadEnvExtraAttributes(t *testing.T) {
	t.Setenv("AGENT_EXTRA_ATTRIBUTES", "prometheus.cluster=demo,team=collector")

	cfg := flagConfig{
		extraAttributes: map[string]string{},
	}

	require.NoError(t, loadEnv(&cfg))

	assert.Equal(t, map[string]string{
		"prometheus.cluster": "demo",
		"team":               "collector",
	}, cfg.extraAttributes)
}
