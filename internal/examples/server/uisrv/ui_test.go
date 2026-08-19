package uisrv

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMutationEndpointsRequirePost(t *testing.T) {
	handler := newUIHandler()
	for _, endpoint := range []string{
		"/save_config",
		"/rotate_client_cert",
		"/opamp_connection_settings",
		"/send_custom_message",
	} {
		t.Run(endpoint, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, endpoint, nil)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			require.Equal(t, http.StatusMethodNotAllowed, response.Code)
			require.Equal(t, http.MethodPost, response.Header().Get("Allow"))
		})
	}
}

func TestPostOnlyAllowsPost(t *testing.T) {
	called := false
	handler := postOnly(func(http.ResponseWriter, *http.Request) {
		called = true
	})

	handler.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/", nil),
	)

	require.True(t, called)
}
