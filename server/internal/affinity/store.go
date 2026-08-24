package affinity

import (
	"database/sql"
	"encoding/json"
	"strings"
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
	db           *sql.DB
}

func NewStore() *Store {
	return newStore(nil)
}

func NewPersistentStore(db *sql.DB) (*Store, error) {
	store := newStore(db)
	if db == nil {
		return store, nil
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS response_affinity (
			response_id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			conversation_id TEXT,
			turn_state TEXT,
			instructions TEXT,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			function_call_ids TEXT NOT NULL DEFAULT '[]',
			recorded_at TEXT NOT NULL
		)
	`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_response_affinity_account_id ON response_affinity(account_id)`); err != nil {
		return nil, err
	}
	if err := store.loadPersisted(); err != nil {
		return nil, err
	}
	return store, nil
}

func newStore(db *sql.DB) *Store {
	return &Store{
		byResponseID: map[string]Binding{},
		db:           db,
	}
}

func (s *Store) Record(responseID, accountID, conversationID, turnState, instructions string, inputTokens int, functionCallIDs []string) error {
	responseID = strings.TrimSpace(responseID)
	accountID = strings.TrimSpace(accountID)
	if responseID == "" || accountID == "" {
		return nil
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
		RecordedAt:      time.Now().UTC(),
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
	if s.db == nil {
		return nil
	}
	return s.persistLocked(responseID, binding)
}

func (s *Store) AccountForResponse(responseID string) string {
	return s.bindingForResponse(responseID).AccountID
}

func (s *Store) ConversationForResponse(responseID string) string {
	return s.bindingForResponse(responseID).ConversationID
}

func (s *Store) TurnStateForResponse(responseID string) string {
	return s.bindingForResponse(responseID).TurnState
}

func (s *Store) InstructionsForResponse(responseID string) string {
	return s.bindingForResponse(responseID).Instructions
}

func (s *Store) InputTokensForResponse(responseID string) int {
	return s.bindingForResponse(responseID).InputTokens
}

func (s *Store) FunctionCallIDsForResponse(responseID string) []string {
	ids := s.bindingForResponse(responseID).FunctionCallIDs
	cloned := make([]string, len(ids))
	copy(cloned, ids)
	return cloned
}

func (s *Store) bindingForResponse(responseID string) Binding {
	responseID = strings.TrimSpace(responseID)
	s.mu.RLock()
	binding, ok := s.byResponseID[responseID]
	db := s.db
	s.mu.RUnlock()
	if ok || db == nil || responseID == "" {
		return binding
	}

	var accountID, conversationID, turnState, instructions, functionCallIDs, recordedAt string
	var inputTokens int
	err := db.QueryRow(`
		SELECT account_id, conversation_id, turn_state, instructions, input_tokens, function_call_ids, recorded_at
		FROM response_affinity
		WHERE response_id = ?
	`, responseID).Scan(&accountID, &conversationID, &turnState, &instructions, &inputTokens, &functionCallIDs, &recordedAt)
	if err != nil {
		return Binding{}
	}
	binding = decodeBinding(accountID, conversationID, turnState, instructions, inputTokens, functionCallIDs, recordedAt)

	s.mu.Lock()
	if existing, exists := s.byResponseID[responseID]; exists {
		binding = existing
	} else {
		s.byResponseID[responseID] = binding
	}
	s.mu.Unlock()
	return binding
}

func (s *Store) loadPersisted() error {
	rows, err := s.db.Query(`
		SELECT response_id, account_id, conversation_id, turn_state, instructions, input_tokens, function_call_ids, recorded_at
		FROM response_affinity
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var responseID, accountID, conversationID, turnState, instructions, functionCallIDs, recordedAt string
		var inputTokens int
		if err := rows.Scan(&responseID, &accountID, &conversationID, &turnState, &instructions, &inputTokens, &functionCallIDs, &recordedAt); err != nil {
			return err
		}
		s.byResponseID[responseID] = decodeBinding(accountID, conversationID, turnState, instructions, inputTokens, functionCallIDs, recordedAt)
	}
	return rows.Err()
}

func (s *Store) persistLocked(responseID string, binding Binding) error {
	functionCallIDs, err := json.Marshal(binding.FunctionCallIDs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO response_affinity (
			response_id, account_id, conversation_id, turn_state, instructions,
			input_tokens, function_call_ids, recorded_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(response_id) DO UPDATE SET
			account_id = excluded.account_id,
			conversation_id = excluded.conversation_id,
			turn_state = excluded.turn_state,
			instructions = excluded.instructions,
			input_tokens = excluded.input_tokens,
			function_call_ids = excluded.function_call_ids,
			recorded_at = excluded.recorded_at
	`, responseID, binding.AccountID, binding.ConversationID, binding.TurnState, binding.Instructions, binding.InputTokens, string(functionCallIDs), binding.RecordedAt.Format(time.RFC3339Nano))
	return err
}

func decodeBinding(accountID, conversationID, turnState, instructions string, inputTokens int, functionCallIDs, recordedAt string) Binding {
	var ids []string
	if strings.TrimSpace(functionCallIDs) != "" {
		_ = json.Unmarshal([]byte(functionCallIDs), &ids)
	}
	var recorded time.Time
	if parsed, err := time.Parse(time.RFC3339Nano, recordedAt); err == nil {
		recorded = parsed
	}
	return Binding{
		AccountID:       accountID,
		ConversationID:  conversationID,
		TurnState:       turnState,
		Instructions:    instructions,
		InputTokens:     inputTokens,
		FunctionCallIDs: ids,
		RecordedAt:      recorded,
	}
}
