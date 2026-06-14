package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jushjay/prism/internal/config"
)

const callbackPort = 1455

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type Session struct {
	CodeVerifier    string
	RedirectURI     string
	ReturnHost      string
	TargetAccountID string
	CreatedAt       time.Time
	Exchanging      bool
}

type OAuthService struct {
	cfg       config.AuthConfig
	client    *http.Client
	mu        sync.Mutex
	sessions  map[string]*Session
	completed map[string]time.Time
	server    *http.Server
}

func NewOAuthService(cfg config.AuthConfig) *OAuthService {
	return &OAuthService{
		cfg: cfg,
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
		sessions:  map[string]*Session{},
		completed: map[string]time.Time{},
	}
}

func (s *OAuthService) Start(returnHost string, targetAccountID string) (authURL string, state string, err error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return "", "", err
	}
	state, err = randomString(16)
	if err != nil {
		return "", "", err
	}
	redirectURI := "http://localhost:1455/auth/callback"
	s.mu.Lock()
	s.sessions[state] = &Session{
		CodeVerifier:    verifier,
		RedirectURI:     redirectURI,
		ReturnHost:      returnHost,
		TargetAccountID: strings.TrimSpace(targetAccountID),
		CreatedAt:       time.Now(),
	}
	s.mu.Unlock()
	if err := s.ensureCallbackServer(); err != nil {
		return "", "", err
	}

	values := []string{
		"response_type=code",
		"client_id=" + url.QueryEscape(s.cfg.OAuthClientID),
		"redirect_uri=" + url.QueryEscape(redirectURI),
		"scope=" + url.QueryEscape("openid profile email offline_access"),
		"code_challenge=" + url.QueryEscape(challenge),
		"code_challenge_method=S256",
		"id_token_add_organizations=true",
		"codex_cli_simplified_flow=true",
		"state=" + url.QueryEscape(state),
		"originator=codex_cli_rs",
	}
	authURL = s.cfg.OAuthAuthEndpoint + "?" + strings.Join(values, "&")
	return authURL, state, nil
}

func (s *OAuthService) TryAcquire(state string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[state]
	if !ok {
		return nil, false
	}
	if time.Since(session.CreatedAt) > 5*time.Minute {
		delete(s.sessions, state)
		return nil, false
	}
	if session.Exchanging {
		return nil, false
	}
	session.Exchanging = true
	return session, true
}

func (s *OAuthService) Release(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[state]; ok {
		session.Exchanging = false
	}
}

func (s *OAuthService) Complete(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, state)
	s.completed[state] = time.Now()
}

func (s *OAuthService) IsCompleted(state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	createdAt, ok := s.completed[state]
	if !ok {
		return false
	}
	if time.Since(createdAt) > 5*time.Minute {
		delete(s.completed, state)
		return false
	}
	return true
}

func (s *OAuthService) ensureCallbackServer() error {
	s.mu.Lock()
	if s.server != nil {
		s.mu.Unlock()
		return nil
	}
	server := &http.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", callbackPort),
		Handler: http.HandlerFunc(s.handleLocalCallback),
	}
	s.server = server
	s.mu.Unlock()

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("[oauth] callback server exited: %v\n", err)
		}
	}()
	return nil
}

func (s *OAuthService) handleLocalCallback(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/auth/callback" {
		http.NotFound(w, r)
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if authErr := r.URL.Query().Get("error"); authErr != "" {
		message := r.URL.Query().Get("error_description")
		if strings.TrimSpace(message) == "" {
			message = authErr
		}
		writeCallbackHTML(w, false, message)
		return
	}
	if state == "" || code == "" {
		writeCallbackHTML(w, false, "Missing code or state")
		return
	}

	session := s.peekSession(state)
	if session == nil {
		if s.IsCompleted(state) {
			writeCallbackHTML(w, true, "")
			return
		}
		writeCallbackHTML(w, false, "Invalid or expired session")
		return
	}

	callbackURL := "http://localhost:1455" + r.URL.RequestURI()
	relayURL := "http://" + session.ReturnHost + "/auth/code-relay"
	body, _ := json.Marshal(map[string]string{"callbackUrl": callbackURL})
	req, err := http.NewRequest(http.MethodPost, relayURL, bytes.NewReader(body))
	if err != nil {
		writeCallbackHTML(w, false, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		writeCallbackHTML(w, false, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		message := strings.TrimSpace(string(raw))
		if message == "" {
			message = fmt.Sprintf("Relay failed with status %d", resp.StatusCode)
		}
		writeCallbackHTML(w, false, message)
		return
	}
	writeCallbackHTML(w, true, "")
}

func (s *OAuthService) peekSession(state string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[state]
	if !ok {
		return nil
	}
	if time.Since(session.CreatedAt) > 5*time.Minute {
		delete(s.sessions, state)
		return nil
	}
	return session
}

func (s *OAuthService) ExchangeCode(code, verifier, redirectURI string) (TokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {s.cfg.OAuthClientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequest(http.MethodPost, s.cfg.OAuthTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return TokenResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		return TokenResponse{}, errors.New(body.String())
	}
	var tokens TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return TokenResponse{}, err
	}
	return tokens, nil
}

func (s *OAuthService) Refresh(refreshToken string) (TokenResponse, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {s.cfg.OAuthClientID},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequest(http.MethodPost, s.cfg.OAuthTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return TokenResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		return TokenResponse{}, errors.New(body.String())
	}
	var tokens TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return TokenResponse{}, err
	}
	return tokens, nil
}

func ImportCLIAuth(raw []byte) (TokenResponse, error) {
	var payload TokenResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return TokenResponse{}, err
	}
	if payload.AccessToken == "" {
		return TokenResponse{}, errors.New("auth.json does not contain access_token")
	}
	return payload, nil
}

func generatePKCE() (verifier string, challenge string, err error) {
	bytesValue := make([]byte, 32)
	if _, err = rand.Read(bytesValue); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(bytesValue)
	digest := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(digest[:])
	return verifier, challenge, nil
}

func randomString(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func CallbackPort() int {
	return callbackPort
}

func writeCallbackHTML(w http.ResponseWriter, success bool, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if success {
		_, _ = io.WriteString(w, `<!doctype html><html><body style="font-family:sans-serif;padding:40px"><h2>Login successful</h2><p>You can close this window.</p><script>if(window.opener){window.opener.postMessage({type:'oauth-callback-success'},'*')}window.close()</script></body></html>`)
		return
	}
	escaped, _ := json.Marshal(message)
	_, _ = io.WriteString(w, `<!doctype html><html><body style="font-family:sans-serif;padding:40px"><h2>Login failed</h2><p>`+message+`</p><script>if(window.opener){window.opener.postMessage({type:'oauth-callback-error',error:`+string(escaped)+`},'*')}</script></body></html>`)
}
