// Package eval is a manual-eval harness for blind A/B model comparison.
//
// A human drives the browser by hand on lmarena.ai, pastes the two outputs
// into the gateway, and the gateway only stores, blinds, and scores. It
// never fetches lmarena.ai itself: no upstream calls, no automation, no
// credential handling. Model labels are sealed per round until explicit
// reveal, so votes stay blind.
package eval

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// newID returns a random 128-bit hex id for evals and rounds.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("eval id: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Vote values accepted by RecordVote.
const (
	VoteA   = "a"
	VoteB   = "b"
	VoteTie = "tie"
)

// Round is one blind comparison: one prompt, two pasted outputs, one vote.
type Round struct {
	ID        string    `json:"id"`
	Prompt    string    `json:"prompt"`
	OutputA   string    `json:"output_a"`
	OutputB   string    `json:"output_b"`
	ModelA    string    `json:"model_a,omitempty"`
	ModelB    string    `json:"model_b,omitempty"`
	Sealed    bool      `json:"sealed"`
	Winner    string    `json:"winner,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	VotedAt   time.Time `json:"voted_at,omitempty"`
}

// Eval is a named set of blind rounds with a derived score.
type Eval struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Rounds    []Round   `json:"rounds"`
}

// Score tallies wins per side and, for revealed rounds, per model.
type Score struct {
	Rounds int                   `json:"rounds"`
	Voted  int                   `json:"voted"`
	WinsA  int                   `json:"wins_a"`
	WinsB  int                   `json:"wins_b"`
	Ties   int                   `json:"ties"`
	Models map[string]ModelScore `json:"models,omitempty"`
}

// ModelScore is the revealed per-model tally.
type ModelScore struct {
	Wins   int `json:"wins"`
	Losses int `json:"losses"`
	Ties   int `json:"ties"`
}

// ScoreOf derives the tally for e. Rounds without a vote are skipped;
// per-model tallies only cover revealed (unsealed, labeled) rounds.
func ScoreOf(e Eval) Score {
	s := Score{Rounds: len(e.Rounds)}
	for _, r := range e.Rounds {
		switch r.Winner {
		case VoteA:
			s.Voted++
			s.WinsA++
		case VoteB:
			s.Voted++
			s.WinsB++
		case VoteTie:
			s.Voted++
			s.Ties++
		default:
			continue
		}
		if r.Sealed || (r.ModelA == "" && r.ModelB == "") {
			continue
		}
		if s.Models == nil {
			s.Models = map[string]ModelScore{}
		}
		a := s.Models[r.ModelA]
		b := s.Models[r.ModelB]
		switch r.Winner {
		case VoteA:
			a.Wins++
			b.Losses++
		case VoteB:
			b.Wins++
			a.Losses++
		case VoteTie:
			a.Ties++
			b.Ties++
		}
		if r.ModelA != "" {
			s.Models[r.ModelA] = a
		}
		if r.ModelB != "" {
			s.Models[r.ModelB] = b
		}
	}
	return s
}

// Store persists evals as one JSON file per eval under Dir. Writes are
// atomic (tmp + rename); an in-memory index guards concurrent access.
type Store struct {
	mu  sync.RWMutex
	dir string
	all map[string]Eval
}

// NewStore loads existing evals from dir (creating it). Corrupt files are
// skipped, never fatal: the harness must not refuse to boot on bad data.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("eval store dir is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("eval store mkdir: %w", err)
	}
	s := &Store{dir: dir, all: map[string]Eval{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("eval store read: %w", err)
	}
	for _, en := range entries {
		if en.IsDir() || filepath.Ext(en.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, en.Name()))
		if err != nil {
			continue
		}
		var e Eval
		if err := json.Unmarshal(data, &e); err != nil || e.ID == "" {
			continue
		}
		s.all[e.ID] = e
	}
	return s, nil
}

// Create starts a new empty eval.
func (s *Store) Create(name string) (Eval, error) {
	if name == "" {
		return Eval{}, errors.New("eval name is empty")
	}
	e := Eval{ID: newID(), Name: name, CreatedAt: time.Now().UTC(), Rounds: []Round{}}
	s.mu.Lock()
	s.all[e.ID] = e
	s.mu.Unlock()
	if err := s.save(e); err != nil {
		return Eval{}, err
	}
	return e, nil
}

// Get returns the eval by id.
func (s *Store) Get(id string) (Eval, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.all[id]
	return e, ok
}

// List returns all evals (rounds omitted for size).
func (s *Store) List() []Eval {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Eval, 0, len(s.all))
	for _, e := range s.all {
		e.Rounds = nil
		out = append(out, e)
	}
	return out
}

// AddRound appends a blind round. When both model labels are given they are
// sealed (hidden from reads) until Reveal.
func (s *Store) AddRound(evalID, prompt, outputA, outputB, modelA, modelB string) (Round, error) {
	if prompt == "" || outputA == "" || outputB == "" {
		return Round{}, errors.New("prompt, output_a and output_b are required")
	}
	s.mu.Lock()
	e, ok := s.all[evalID]
	if !ok {
		s.mu.Unlock()
		return Round{}, errors.New("eval not found")
	}
	r := Round{
		ID:        newID(),
		Prompt:    prompt,
		OutputA:   outputA,
		OutputB:   outputB,
		ModelA:    modelA,
		ModelB:    modelB,
		Sealed:    modelA != "" && modelB != "",
		CreatedAt: time.Now().UTC(),
	}
	e.Rounds = append(e.Rounds, r)
	s.all[evalID] = e
	s.mu.Unlock()
	if err := s.save(e); err != nil {
		return Round{}, err
	}
	return r, nil
}

// RecordVote sets the winner of a round once ("a", "b" or "tie").
func (s *Store) RecordVote(evalID, roundID, winner string) (Round, error) {
	if winner != VoteA && winner != VoteB && winner != VoteTie {
		return Round{}, errors.New(`winner must be "a", "b" or "tie"`)
	}
	s.mu.Lock()
	e, ok := s.all[evalID]
	if !ok {
		s.mu.Unlock()
		return Round{}, errors.New("eval not found")
	}
	for i, r := range e.Rounds {
		if r.ID != roundID {
			continue
		}
		if r.Winner != "" {
			s.mu.Unlock()
			return Round{}, errors.New("round already voted")
		}
		e.Rounds[i].Winner = winner
		e.Rounds[i].VotedAt = time.Now().UTC()
		r = e.Rounds[i]
		s.all[evalID] = e
		s.mu.Unlock()
		if err := s.save(e); err != nil {
			return Round{}, err
		}
		return r, nil
	}
	s.mu.Unlock()
	return Round{}, errors.New("round not found")
}

// Reveal unseals all labeled rounds of the eval, returning it with labels.
func (s *Store) Reveal(evalID string) (Eval, error) {
	s.mu.Lock()
	e, ok := s.all[evalID]
	if !ok {
		s.mu.Unlock()
		return Eval{}, errors.New("eval not found")
	}
	for i := range e.Rounds {
		e.Rounds[i].Sealed = false
	}
	s.all[evalID] = e
	s.mu.Unlock()
	if err := s.save(e); err != nil {
		return Eval{}, err
	}
	return e, nil
}

// Blind returns a copy of e with sealed model labels stripped.
func Blind(e Eval) Eval {
	out := e
	out.Rounds = make([]Round, len(e.Rounds))
	for i, r := range e.Rounds {
		if r.Sealed {
			r.ModelA = ""
			r.ModelB = ""
		}
		out.Rounds[i] = r
	}
	return out
}

func (s *Store) save(e Eval) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, e.ID+"-*.tmp")
	if err != nil {
		return fmt.Errorf("eval store write: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("eval store write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("eval store write: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, e.ID+".json")); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("eval store write: %w", err)
	}
	return nil
}
