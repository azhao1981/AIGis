package audit

import (
	"bufio"
	"encoding/json"
	"os"
)

// QueryOptions filters a Query over the audit JSONL file.
type QueryOptions struct {
	// Limit caps the number of records returned (most recent first).
	// <=0 or >MaxQueryLimit falls back to DefaultQueryLimit / MaxQueryLimit.
	Limit int
	// RuleType, when non-empty, keeps only records containing a hit of that
	// rule type (e.g. "Email").
	RuleType string
}

const (
	DefaultQueryLimit = 100
	MaxQueryLimit     = 1000
)

// Query reads the audit JSONL file at path and returns the most recent records
// (newest first), optionally filtered by rule type. A missing file yields an
// empty result, not an error (auditing may be disabled or nothing masked yet).
// Corrupt lines are skipped so one bad write never breaks the whole view.
// Records contain only what the auditor wrote: rule types, counts, placeholders
// and partially-masked previews — never plaintext secrets.
func Query(path string, opts QueryOptions) ([]Record, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultQueryLimit
	}
	if limit > MaxQueryLimit {
		limit = MaxQueryLimit
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Record{}, nil
		}
		return nil, err
	}
	defer f.Close()

	// Ring buffer of the last `limit` matching records, in file (oldest-first)
	// order; reversed to newest-first at the end.
	tail := make([]Record, 0, limit)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var rec Record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		if opts.RuleType != "" {
			if _, ok := rec.ByType[opts.RuleType]; !ok {
				continue
			}
		}
		if len(tail) == limit {
			tail = tail[1:]
		}
		tail = append(tail, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	out := make([]Record, 0, len(tail))
	for i := len(tail) - 1; i >= 0; i-- {
		out = append(out, tail[i])
	}
	return out, nil
}
