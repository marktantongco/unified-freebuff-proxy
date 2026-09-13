package eval

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Bulk-import bounds: one request must stay a paste-size operation, not a
// dataset mirror. Larger migrations belong in repeated calls.
const (
	maxImportRecords = 500
	maxImportBytes   = 1 << 20 // 1MB
)

// ImportRecord is one pasted round for bulk import. Winner is optional
// (empty = unvoted); when set it must be "a", "b" or "tie".
type ImportRecord struct {
	Prompt  string `json:"prompt"`
	OutputA string `json:"output_a"`
	OutputB string `json:"output_b"`
	ModelA  string `json:"model_a"`
	ModelB  string `json:"model_b"`
	Winner  string `json:"winner"`
}

func (r ImportRecord) validate() error {
	if r.Prompt == "" || r.OutputA == "" || r.OutputB == "" {
		return errors.New("prompt, output_a and output_b are required")
	}
	if r.Winner != "" && r.Winner != VoteA && r.Winner != VoteB && r.Winner != VoteTie {
		return fmt.Errorf("winner must be %q, %q or %q", VoteA, VoteB, VoteTie)
	}
	return nil
}

// ParseJSONL parses one JSON ImportRecord per non-blank line.
func ParseJSONL(data string) ([]ImportRecord, error) {
	if len(data) > maxImportBytes {
		return nil, fmt.Errorf("payload exceeds %d bytes", maxImportBytes)
	}
	var out []ImportRecord
	for i, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r ImportRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if err := r.validate(); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, r)
	}
	return checkImportSize(out)
}

// ParseCSV parses records with a header row. Required columns:
// prompt, output_a, output_b. Optional: model_a, model_b, winner.
// Unknown columns are rejected to catch header typos early.
func ParseCSV(data string) ([]ImportRecord, error) {
	if len(data) > maxImportBytes {
		return nil, fmt.Errorf("payload exceeds %d bytes", maxImportBytes)
	}
	rd := csv.NewReader(strings.NewReader(data))
	rd.FieldsPerRecord = -1
	rows, err := rd.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("csv parse: %w", err)
	}
	if len(rows) < 2 {
		return nil, errors.New("csv needs a header row plus at least one record")
	}
	cols := map[string]int{}
	for i, h := range rows[0] {
		h = strings.ToLower(strings.TrimSpace(h))
		switch h {
		case "prompt", "output_a", "output_b", "model_a", "model_b", "winner":
			cols[h] = i
		default:
			return nil, fmt.Errorf("unknown column %q", rows[0][i])
		}
	}
	for _, need := range []string{"prompt", "output_a", "output_b"} {
		if _, ok := cols[need]; !ok {
			return nil, fmt.Errorf("missing required column %q", need)
		}
	}
	var out []ImportRecord
	get := func(row []string, name string) string {
		if i, ok := cols[name]; ok && i < len(row) {
			return row[i]
		}
		return ""
	}
	for i, row := range rows[1:] {
		r := ImportRecord{
			Prompt:  get(row, "prompt"),
			OutputA: get(row, "output_a"),
			OutputB: get(row, "output_b"),
			ModelA:  get(row, "model_a"),
			ModelB:  get(row, "model_b"),
			Winner:  strings.ToLower(strings.TrimSpace(get(row, "winner"))),
		}
		if err := r.validate(); err != nil {
			return nil, fmt.Errorf("row %d: %w", i+2, err)
		}
		out = append(out, r)
	}
	return checkImportSize(out)
}

func checkImportSize(out []ImportRecord) ([]ImportRecord, error) {
	if len(out) == 0 {
		return nil, errors.New("no records found")
	}
	if len(out) > maxImportRecords {
		return nil, fmt.Errorf("%d records exceeds limit %d", len(out), maxImportRecords)
	}
	return out, nil
}

// Import appends validated records as rounds in one locked save. Rounds
// with a winner arrive pre-voted (same human paste, fewer round trips).
func (s *Store) Import(evalID string, recs []ImportRecord) ([]Round, error) {
	if len(recs) == 0 {
		return nil, errors.New("no records to import")
	}
	if len(recs) > maxImportRecords {
		return nil, fmt.Errorf("%d records exceeds limit %d", len(recs), maxImportRecords)
	}
	now := time.Now().UTC()
	s.mu.Lock()
	e, ok := s.all[evalID]
	if !ok {
		s.mu.Unlock()
		return nil, errors.New("eval not found")
	}
	out := make([]Round, 0, len(recs))
	for _, r := range recs {
		if err := r.validate(); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		round := Round{
			ID:        newID(),
			Prompt:    r.Prompt,
			OutputA:   r.OutputA,
			OutputB:   r.OutputB,
			ModelA:    r.ModelA,
			ModelB:    r.ModelB,
			Sealed:    r.ModelA != "" && r.ModelB != "",
			Winner:    r.Winner,
			CreatedAt: now,
		}
		if r.Winner != "" {
			round.VotedAt = now
		}
		e.Rounds = append(e.Rounds, round)
		out = append(out, round)
	}
	s.all[evalID] = e
	s.mu.Unlock()
	if err := s.save(e); err != nil {
		return nil, err
	}
	return out, nil
}
