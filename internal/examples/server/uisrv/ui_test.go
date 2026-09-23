package uisrv

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMutationEndpointsRequirePostAndSameOrigin(t *testing.T) {
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

			crossOriginRequest := httptest.NewRequest(http.MethodPost, endpoint, nil)
			crossOriginRequest.Header.Set("Origin", "https://other.example.com")
			crossOriginResponse := httptest.NewRecorder()

			handler.ServeHTTP(crossOriginResponse, crossOriginRequest)

			require.Equal(t, http.StatusForbidden, crossOriginResponse.Code)

			sameOriginRequest := httptest.NewRequest(http.MethodPost, endpoint, nil)
			sameOriginRequest.Header.Set("Origin", "http://example.com")
			sameOriginResponse := httptest.NewRecorder()

			handler.ServeHTTP(sameOriginResponse, sameOriginRequest)

			require.NotEqual(t, http.StatusForbidden, sameOriginResponse.Code)
		})
	}
}

func TestHasSameOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "no origin", want: true},
		{name: "same HTTP origin", origin: "http://example.com", want: true},
		{name: "same HTTPS origin", origin: "https://example.com", want: true},
		{name: "different origin", origin: "https://other.example.com", want: false},
		{name: "different port", origin: "https://example.com:8443", want: false},
		{name: "unsupported scheme", origin: "file://example.com", want: false},
		{name: "invalid origin", origin: "://example.com", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://example.com/save_config", nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}

			require.Equal(t, test.want, hasSameOrigin(request))
		})
	}
}
