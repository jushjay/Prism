package affinity

import "testing"

func TestStoreRecordAndLookup(t *testing.T) {
	store := NewStore()
	store.Record("resp_123", "acct_1", "conv_1", "turn_1", "instr_1", 123, []string{"call_1"})

	if got := store.AccountForResponse("resp_123"); got != "acct_1" {
		t.Fatalf("expected account acct_1, got %q", got)
	}
	if got := store.ConversationForResponse("resp_123"); got != "conv_1" {
		t.Fatalf("expected conversation conv_1, got %q", got)
	}
	if got := store.TurnStateForResponse("resp_123"); got != "turn_1" {
		t.Fatalf("expected turnState turn_1, got %q", got)
	}
	if got := store.InstructionsForResponse("resp_123"); got != "instr_1" {
		t.Fatalf("expected instructions instr_1, got %q", got)
	}
	if got := store.InputTokensForResponse("resp_123"); got != 123 {
		t.Fatalf("expected inputTokens 123, got %d", got)
	}
	ids := store.FunctionCallIDsForResponse("resp_123")
	if len(ids) != 1 || ids[0] != "call_1" {
		t.Fatalf("expected function call ids [call_1], got %#v", ids)
	}
}

func TestStoreRecordMergesFunctionCallIDs(t *testing.T) {
	store := NewStore()
	store.Record("resp_123", "acct_1", "conv_1", "turn_1", "instr_1", 100, []string{"call_1"})
	store.Record("resp_123", "acct_1", "", "", "", 0, []string{"call_2", "call_1"})

	ids := store.FunctionCallIDsForResponse("resp_123")
	if len(ids) != 2 {
		t.Fatalf("expected 2 function call ids, got %#v", ids)
	}
	if got := store.InstructionsForResponse("resp_123"); got != "instr_1" {
		t.Fatalf("expected instructions preserved, got %q", got)
	}
}
