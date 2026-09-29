package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jushjay/prism/internal/codex"
	"github.com/jushjay/prism/internal/config"
	"github.com/jushjay/prism/internal/reasoning"
)

const healthPath = "/_sidecar/health"

var downstreamHeartbeatInterval = 10 * time.Second

type Server struct {
	cfg      config.Config
	engine   *gin.Engine
	proxy    *httputil.ReverseProxy
	upstream *codex.Client
	resolver *reasoning.Resolver
	audit    *auditManager
}

func NewServer() (*Server, error) {
	cfg := config.Load()
	resolver, err := reasoning.Load(cfg.Sidecar.ConfigFile, cfg.Sidecar.ConfigFileExplicit)
	if err != nil {
		return nil, fmt.Errorf("load sidecar config: %w", err)
	}
	audit, err := newAuditManager(cfg.Audit)
	if err != nil {
		return nil, err
	}
	s, err := newServer(cfg, resolver, audit)
	if err != nil {
		_ = audit.Close()
		return nil, err
	}
	return s, nil
}

func newServer(cfg config.Config, resolver *reasoning.Resolver, audit *auditManager) (*Server, error) {
	target, err := url.Parse(cfg.Upstream.BaseURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("invalid SUB2API_BASE_URL %q", cfg.Upstream.BaseURL)
	}
	transport := newUpstreamTransport()
	s := &Server{
		cfg:      cfg,
		engine:   gin.New(),
		proxy:    newReverseProxy(target, cfg.Upstream.PreserveHost, transport),
		upstream: codex.NewClient(cfg.Upstream, transport),
		resolver: resolver,
		audit:    audit,
	}
	s.engine.Use(gin.Recovery())
	s.engine.GET(healthPath, s.handleHealth)
	s.engine.POST("/v1/chat/completions", s.handleCursorChatCompletions)
	s.engine.POST("/v1/responses", s.handleCursorResponses)
	return s, nil
}

func (s *Server) Handler() http.Handler {
	return withAccessLog(http.HandlerFunc(s.dispatch))
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == healthPath || isCursorRewriteRequest(r) {
		s.engine.ServeHTTP(w, r)
		return
	}
	s.proxy.ServeHTTP(w, r)
}

func isCursorRewriteRequest(r *http.Request) bool {
	if r.Method != http.MethodPost || !isCursorUserAgent(r.UserAgent()) {
		return false
	}
	switch r.URL.Path {
	case "/v1/chat/completions", "/v1/responses":
		return true
	default:
		return false
	}
}

func (s *Server) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"ok":             true,
		"service":        "sub2api-cursor-sidecar",
		"upstream":       s.cfg.Upstream.BaseURL,
		"model_mappings": s.resolver.MappingCount(),
		"time":           time.Now().UTC(),
	})
}

func (s *Server) Run() error {
	defer func() {
		if s.audit != nil {
			_ = s.audit.Close()
		}
	}()
	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(gin.DefaultWriter, "[sidecar] listening on %s, upstream=%s, model_mappings=%d\n", addr, s.cfg.Upstream.BaseURL, s.resolver.MappingCount())
		errCh <- srv.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case err := <-errCh:
		return err
	case sig := <-signals:
		fmt.Fprintf(gin.DefaultWriter, "[sidecar] received %s, shutting down\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			return err
		}
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func newUpstreamTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 64
	return transport
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

// Flush and Hijack are implemented directly because gin asserts these
// interfaces on the underlying writer instead of using http.ResponseController.
func (r *statusRecorder) Flush() {
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if r.status == 0 {
		r.status = http.StatusSwitchingProtocols
	}
	return http.NewResponseController(r.ResponseWriter).Hijack()
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		if r.URL.Path == healthPath {
			return
		}
		fmt.Fprintln(gin.DefaultWriter, buildAccessLogLine(r, recorder.status, recorder.bytes, start))
	})
}

func buildAccessLogLine(r *http.Request, status int, bytesSent int64, start time.Time) string {
	route := "proxy"
	if isCursorRewriteRequest(r) {
		route = "cursor"
	}
	return fmt.Sprintf(
		`time=%q client_ip=%q method=%q uri=%q route=%q status=%d latency=%q bytes_sent=%d ua=%q`,
		start.Format(time.RFC3339),
		clientIP(r),
		r.Method,
		r.URL.RequestURI(),
		route,
		status,
		time.Since(start).Round(time.Millisecond),
		bytesSent,
		r.UserAgent(),
	)
}

func clientIP(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Real-IP")); value != "" {
		return value
	}
	if value := r.Header.Get("X-Forwarded-For"); value != "" {
		if first, _, _ := strings.Cut(value, ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
