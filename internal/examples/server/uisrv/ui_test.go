package uisrv

import (
	"net/url"
	"testing"

	"github.com/google/uuid"
	exampleshtml "github.com/open-telemetry/opamp-go/internal/examples/html"
	"github.com/stretchr/testify/require"
)

func TestAgentPathPreservesProxyPrefix(t *testing.T) {
	instanceID := uuid.MustParse("01234567-89ab-cdef-0123-456789abcdef")
	requestURL, err := url.Parse("https://example.com/opamp/save_config")
	require.NoError(t, err)
	redirectURL, err := url.Parse(agentPath(instanceID))
	require.NoError(t, err)

	require.Equal(
		t,
		"https://example.com/opamp/agent?instanceid=01234567-89ab-cdef-0123-456789abcdef",
		requestURL.ResolveReference(redirectURL).String(),
	)
}

func TestAgentTemplateUsesRelativeInternalURLs(t *testing.T) {
	template, err := exampleshtml.HtmlFS.ReadFile("html/agent.html")
	require.NoError(t, err)

	require.NotContains(t, string(template), `href="/"`)
	require.NotContains(t, string(template), `action="/`)
}
