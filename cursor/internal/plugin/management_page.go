package plugin

import (
	_ "embed"
	"net/http"
)

//go:embed assets/management.html
var managementPage []byte

func managementPageResponse() managementResponse {
	return managementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Cache-Control":           []string{"no-store"},
			"Content-Security-Policy": []string{"default-src 'none'; connect-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"},
			"Content-Type":            []string{"text/html; charset=utf-8"},
			"X-Content-Type-Options":  []string{"nosniff"},
			"Referrer-Policy":         []string{"no-referrer"},
		},
		Body: append([]byte(nil), managementPage...),
	}
}
