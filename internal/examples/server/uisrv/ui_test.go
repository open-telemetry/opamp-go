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
			getRequest := httptest.NewRequest(http.MethodGet, endpoint, nil)
			getResponse := httptest.NewRecorder()

			handler.ServeHTTP(getResponse, getRequest)

			require.Equal(t, http.StatusMethodNotAllowed, getResponse.Code)
			require.Equal(t, http.MethodPost, getResponse.Header().Get("Allow"))

			postRequest := httptest.NewRequest(http.MethodPost, endpoint, nil)
			postResponse := httptest.NewRecorder()

			handler.ServeHTTP(postResponse, postRequest)

			require.NotEqual(t, http.StatusMethodNotAllowed, postResponse.Code)
		})
	}
}
