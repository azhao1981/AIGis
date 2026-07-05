package audit

import (
	"os"
	"path/filepath"
	"testing"
)

// writeAudit records n entries via the real Auditor so Query reads the exact
// on-disk format Record writes.
func writeAudit(t *testing.T, path string, entries [][3]string) {
	t.Helper()
	a, err := New(path, true, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Close()
	for i, e := range entries {
		ctx := newCtxWithDetections("req"+string(rune('a'+i)), [][3]string{e})
		a.Record(ctx)
	}
}

// TestQueryNewestFirstAndLimit: Query returns the most recent records first
// and honors the limit.
func TestQueryNewestFirstAndLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeAudit(t, path, [][3]string{
		{"Email", "__AIGIS_SEC_aaaaaaaaaaaa__", "a***a"},
		{"Mobile Phone", "__AIGIS_SEC_bbbbbbbbbbbb__", "1***0"},
		{"Email", "__AIGIS_SEC_cccccccccccc__", "c***c"},
	})

	recs, err := Query(path, QueryOptions{Limit: 2})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("len = %d, want 2", len(recs))
	}
	if recs[0].RequestID != "reqc" || recs[1].RequestID != "reqb" {
		t.Errorf("order wrong: got %s, %s (want reqc, reqb)", recs[0].RequestID, recs[1].RequestID)
	}
}

// TestQueryRuleTypeFilter: only records containing the given rule type return.
func TestQueryRuleTypeFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeAudit(t, path, [][3]string{
		{"Email", "__AIGIS_SEC_aaaaaaaaaaaa__", "a***a"},
		{"Mobile Phone", "__AIGIS_SEC_bbbbbbbbbbbb__", "1***0"},
	})

	recs, err := Query(path, QueryOptions{RuleType: "Mobile Phone"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 1 || recs[0].ByType["Mobile Phone"] != 1 {
		t.Errorf("filter failed, got %+v", recs)
	}
}

// TestQueryMissingFileAndCorruptLine: a missing file is empty (not an error);
// a corrupt line is skipped without failing the whole read.
func TestQueryMissingFileAndCorruptLine(t *testing.T) {
	recs, err := Query(filepath.Join(t.TempDir(), "nope.jsonl"), QueryOptions{})
	if err != nil || len(recs) != 0 {
		t.Errorf("missing file: recs=%v err=%v, want empty+nil", recs, err)
	}

	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeAudit(t, path, [][3]string{
		{"Email", "__AIGIS_SEC_aaaaaaaaaaaa__", "a***a"},
	})
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("{corrupt json\n")
	f.Close()
	writeAudit(t, path, [][3]string{
		{"Email", "__AIGIS_SEC_bbbbbbbbbbbb__", "b***b"},
	})

	recs, err = Query(path, QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("corrupt line should be skipped, got %d records", len(recs))
	}
}
