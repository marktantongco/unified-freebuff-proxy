package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreBlindVoteRevealScore(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	e, err := s.Create("battle-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	r, err := s.AddRound(e.ID, "capital of France?", "Paris", "Paris!", "zenith", "summit")
	if err != nil {
		t.Fatalf("AddRound: %v", err)
	}
	if !r.Sealed {
		t.Fatal("labeled round must be sealed")
	}
	got, _ := s.Get(e.ID)
	if blinded := Blind(got); blinded.Rounds[0].ModelA != "" || blinded.Rounds[0].ModelB != "" {
		t.Fatal("Blind must strip sealed labels")
	}
	if _, err := s.RecordVote(e.ID, r.ID, VoteA); err != nil {
		t.Fatalf("RecordVote: %v", err)
	}
	if _, err := s.RecordVote(e.ID, r.ID, VoteB); err == nil {
		t.Fatal("double vote must fail")
	}
	got, _ = s.Get(e.ID)
	sc := ScoreOf(got)
	if sc.Voted != 1 || sc.WinsA != 1 {
		t.Fatalf("bad blind score: %+v", sc)
	}
	if len(sc.Models) != 0 {
		t.Fatalf("sealed rounds must not score models: %+v", sc)
	}
	rev, err := s.Reveal(e.ID)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if rev.Rounds[0].ModelA != "zenith" {
		t.Fatal("reveal must restore labels")
	}
	sc = ScoreOf(rev)
	if sc.Models["zenith"].Wins != 1 || sc.Models["summit"].Losses != 1 {
		t.Fatalf("bad revealed score: %+v", sc)
	}
	// Persistence: reload from disk.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got2, ok := s2.Get(e.ID)
	if !ok || len(got2.Rounds) != 1 || got2.Rounds[0].Winner != VoteA {
		t.Fatalf("reload lost data: %+v", got2)
	}
	if _, err := os.Stat(filepath.Join(dir, e.ID+".json")); err != nil {
		t.Fatalf("eval file missing: %v", err)
	}
}

func TestStoreValidation(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if _, err := s.Create(""); err == nil {
		t.Fatal("empty name must fail")
	}
	e, _ := s.Create("x")
	if _, err := s.AddRound(e.ID, "", "a", "b", "", ""); err == nil {
		t.Fatal("empty prompt must fail")
	}
	r, _ := s.AddRound(e.ID, "p", "a", "b", "", "")
	if r.Sealed {
		t.Fatal("unlabeled round must not be sealed")
	}
	if _, err := s.RecordVote(e.ID, r.ID, "c"); err == nil {
		t.Fatal("bad winner must fail")
	}
	if _, err := s.RecordVote("nope", r.ID, VoteA); err == nil {
		t.Fatal("unknown eval must fail")
	}
}
