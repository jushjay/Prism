package affinity

import (
	"sync"
	"time"
)

type Binding struct {
	AccountID       string
	ConversationID  string
	TurnState       string
	Instructions    string
	InputTokens     int
	FunctionCallIDs []string
	RecordedAt      time.Time
}

type Store struct {
	mu           sync.RWMutex
	byResponseID map[string]Binding
}

func NewStore() *Store {
	return &Store{
		byResponseID: map[string]Binding{},
	}
}

func (s *Store) Record(responseID, accountID, conversationID, turnState, instructions string, inputTokens int, functionCallIDs []string) {
	if responseID == "" || accountID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.byResponseID[responseID]
	mergedFunctionCallIDs := existing.FunctionCallIDs
	if len(functionCallIDs) > 0 {
		seen := map[string]struct{}{}
		var merged []string
		for _, id := range existing.FunctionCallIDs {
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			merged = append(merged, id)
		}
		for _, id := range functionCallIDs {
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			merged = append(merged, id)
		}
		mergedFunctionCallIDs = merged
	}
	binding := Binding{
		AccountID:       accountID,
		ConversationID:  conversationID,
		TurnState:       turnState,
		Instructions:    instructions,
		InputTokens:     inputTokens,
		FunctionCallIDs: mergedFunctionCallIDs,
		RecordedAt:      time.Now(),
	}
	if binding.ConversationID == "" {
		binding.ConversationID = existing.ConversationID
	}
	if binding.TurnState == "" {
		binding.TurnState = existing.TurnState
	}
	if binding.Instructions == "" {
		binding.Instructions = existing.Instructions
	}
	if binding.InputTokens == 0 {
		binding.InputTokens = existing.InputTokens
	}
	s.byResponseID[responseID] = binding
}

func (s *Store) AccountForResponse(responseID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byResponseID[responseID].AccountID
}

func (s *Store) ConversationForResponse(responseID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byResponseID[responseID].ConversationID
}

func (s *Store) TurnStateForResponse(responseID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byResponseID[responseID].TurnState
}

func (s *Store) InstructionsForResponse(responseID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byResponseID[responseID].Instructions
}

func (s *Store) InputTokensForResponse(responseID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byResponseID[responseID].InputTokens
}

func (s *Store) FunctionCallIDsForResponse(responseID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.byResponseID[responseID].FunctionCallIDs
	cloned := make([]string, len(ids))
	copy(cloned, ids)
	return cloned
}
