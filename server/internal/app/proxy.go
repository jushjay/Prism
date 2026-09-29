package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/gin-gonic/gin"
)

// forwardedHeaders are stripped by httputil.ReverseProxy in Rewrite mode.
// nginx already sets them with the real client values, so they are copied
// back verbatim instead of being regenerated from the loopback connection.
var forwardedHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"}

func newReverseProxy(target *url.URL, preserveHost bool, transport http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			if preserveHost {
				r.Out.Host = r.In.Host
			}
			for _, key := range forwardedHeaders {
				if values := r.In.Header.Values(key); len(values) > 0 {
					r.Out.Header[key] = append([]string(nil), values...)
				}
			}
		},
		Transport:     transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			fmt.Fprintf(gin.DefaultErrorWriter, "[proxy] %s %s failed: %v\n", r.Method, r.URL.RequestURI(), err)
			writeJSONError(w, http.StatusBadGateway, "sub2api unreachable: "+err.Error())
		},
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "server_error",
		},
	})
}
