package auth

import (
	"log"
	"time"
)

type RefreshScheduler struct {
	pool   *AccountPool
	oauth  *OAuthService
	margin time.Duration
	stopCh chan struct{}
}

func NewRefreshScheduler(pool *AccountPool, oauth *OAuthService, margin time.Duration) *RefreshScheduler {
	return &RefreshScheduler{
		pool:   pool,
		oauth:  oauth,
		margin: margin,
		stopCh: make(chan struct{}),
	}
}

func (s *RefreshScheduler) Start() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.tick()
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *RefreshScheduler) Stop() {
	close(s.stopCh)
}

func (s *RefreshScheduler) tick() {
	accounts := s.pool.ActiveForRefresh()
	now := time.Now()
	limit := s.poolSummaryConcurrency()
	sem := make(chan struct{}, limit)
	for _, snapshot := range accounts {
		if snapshot.RefreshToken == "" {
			continue
		}
		if snapshot.ExpiresAt.After(now.Add(s.margin)) {
			continue
		}
		account, ok := s.pool.BeginRefresh(snapshot.ID)
		if !ok {
			continue
		}
		sem <- struct{}{}
		go func(account Account) {
			defer func() { <-sem }()
			tokens, err := s.oauth.Refresh(account.RefreshToken)
			if err != nil {
				log.Printf("[refresh] failed for %s: %v", account.ID, err)
				_ = s.pool.UpdateStatus(account.ID, StatusExpired)
				return
			}
			expiresAt := time.Now().Add(55 * time.Minute)
			if tokens.ExpiresIn > 0 {
				expiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
			}
			if err := s.pool.UpdateTokens(account.ID, tokens.AccessToken, tokens.RefreshToken, expiresAt); err != nil {
				log.Printf("[refresh] failed to persist account %s: %v", account.ID, err)
			}
		}(account)
	}
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}
}

func (s *RefreshScheduler) poolSummaryConcurrency() int {
	if s.pool.cfg.RefreshConcurrency > 0 {
		return s.pool.cfg.RefreshConcurrency
	}
	return 2
}
