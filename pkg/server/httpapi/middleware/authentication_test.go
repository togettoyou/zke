package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestIsHTTPS(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		nativeTLS bool
		forwarded string
		want      bool
	}{
		{name: "plain HTTP"},
		{name: "native TLS", nativeTLS: true, want: true},
		{name: "gateway HTTPS", forwarded: "https", want: true},
		{name: "gateway HTTPS uppercase", forwarded: "HTTPS", want: true},
		// Each proxy appends to the header, so only the browser-facing entry
		// describes the connection whose cookies are at stake.
		{name: "proxy chain from HTTPS", forwarded: "https, http", want: true},
		{name: "proxy chain from HTTP", forwarded: "http, https"},
		{name: "gateway HTTP", forwarded: "http"},
		{name: "empty header", forwarded: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			if testCase.nativeTLS {
				request.TLS = &tls.ConnectionState{}
			}
			if testCase.forwarded != "" {
				request.Header.Set("X-Forwarded-Proto", testCase.forwarded)
			}
			if got := RequestIsHTTPS(request); got != testCase.want {
				t.Fatalf("RequestIsHTTPS() = %t, want %t", got, testCase.want)
			}
		})
	}
}
