package auth

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

type DashboardSession struct {
	ID        string
	ExpiresAt time.Time
}

type DashboardSessions struct {
	mu       sync.RWMutex
	sessions map[string]DashboardSession
	ttl      time.Duration
}

func NewDashboardSessions(ttl time.Duration) *DashboardSessions {
	return &DashboardSessions{
		sessions: map[string]DashboardSession{},
		ttl:      ttl,
	}
}

func (d *DashboardSessions) Create() DashboardSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	session := DashboardSession{
		ID:        uuid.NewString(),
		ExpiresAt: time.Now().Add(d.ttl),
	}
	d.sessions[session.ID] = session
	return session
}

func (d *DashboardSessions) Valid(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	session, ok := d.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(session.ExpiresAt) {
		delete(d.sessions, id)
		return false
	}
	session.ExpiresAt = time.Now().Add(d.ttl)
	d.sessions[id] = session
	return true
}

func (d *DashboardSessions) Delete(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, id)
}
