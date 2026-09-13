package eval

import (
	"testing"
)

func TestParseJSONL(t *testing.T) {
	recs, err := ParseJSONL("{\"prompt\":\"p\",\"output_a\":\"a\",\"output_b\":\"b\",\"model_a\":\"m1\",\"model_b\":\"m2\",\"winner\":\"a\"}\n\n{\"prompt\":\"q\",\"output_a\":\"a\",\"output_b\":\"b\"}\n")
	if err != nil {
		t.Fatalf("ParseJSONL: %v", err)
	}
	if len(recs) != 2 || recs[0].Winner != VoteA || recs[1].Winner != "" {
		t.Fatalf("bad records: %+v", recs)
	}
	if _, err := ParseJSONL("{\"prompt\":\"p\"}\n"); err == nil {
		t.Fatal("missing outputs must fail")
	}
	if _, err := ParseJSONL("{\"prompt\":\"p\",\"output_a\":\"a\",\"output_b\":\"b\",\"winner\":\"c\"}\n"); err == nil {
		t.Fatal("bad winner must fail")
	}
	if _, err := ParseJSONL("not json\n"); err == nil {
		t.Fatal("bad json must fail with line number")
	}
	if _, err := ParseJSONL(""); err == nil {
		t.Fatal("empty payload must fail")
	}
}

func TestParseCSV(t *testing.T) {
	data := "prompt,output_a,output_b,model_a,model_b,winner\n\"2+2?\",\"4\",\"four\",zenith,summit,b\n\"sky?\",\"blue\",\"bleu\",,,\n"
	recs, err := ParseCSV(data)
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	if len(recs) != 2 || recs[0].ModelA != "zenith" || recs[0].Winner != VoteB || recs[1].ModelA != "" {
		t.Fatalf("bad records: %+v", recs)
	}
	if _, err := ParseCSV("prompt,output_a\np,a\n"); err == nil {
		t.Fatal("missing output_b column must fail")
	}
	if _, err := ParseCSV("prompt,output_a,output_b,typo\np,a,b,x\n"); err == nil {
		t.Fatal("unknown column must fail")
	}
	if _, err := ParseCSV("prompt,output_a,output_b\n"); err == nil {
		t.Fatal("header-only must fail")
	}
}

func TestImportAtomic(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	e, _ := s.Create("bulk")
	recs, _ := ParseJSONL("{\"prompt\":\"p1\",\"output_a\":\"a\",\"output_b\":\"b\",\"model_a\":\"m1\",\"model_b\":\"m2\",\"winner\":\"tie\"}\n{\"prompt\":\"p2\",\"output_a\":\"a\",\"output_b\":\"b\"}\n")
	rounds, err := s.Import(e.ID, recs)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(rounds) != 2 || !rounds[0].Sealed || rounds[0].Winner != VoteTie || rounds[1].Sealed {
		t.Fatalf("bad import: %+v", rounds)
	}
	got, _ := s.Get(e.ID)
	if len(got.Rounds) != 2 || ScoreOf(got).Ties != 1 {
		t.Fatalf("score after import wrong: %+v", got)
	}
	// One bad record rejects the whole batch: no partial writes.
	bad := []ImportRecord{{Prompt: "ok", OutputA: "a", OutputB: "b"}, {}}
	if _, err := s.Import(e.ID, bad); err == nil {
		t.Fatal("bad batch must fail")
	}
	got, _ = s.Get(e.ID)
	if len(got.Rounds) != 2 {
		t.Fatalf("failed import must not append: %d rounds", len(got.Rounds))
	}
	if _, err := s.Import("nope", recs); err == nil {
		t.Fatal("unknown eval must fail")
	}
}
