package eval

import "testing"

func TestBTRatingsOrdering(t *testing.T) {
	matches := []btMatch{
		{a: "a", b: "b", outcome: 1},
		{a: "a", b: "b", outcome: 1},
		{a: "b", b: "c", outcome: 1},
	}
	r := btRatings(matches)
	if !(r["a"] > r["b"] && r["b"] > r["c"]) {
		t.Fatalf("want a>b>c, got %+v", r)
	}
	// Elo-400 scale: P(a beats b) from rating gap must match sigmoid.
	gap := r["a"] - r["b"]
	t.Logf("ratings=%+v gap(a,b)=%.1f", r, gap)
	if gap <= 0 || gap > 400 {
		t.Fatalf("implausible gap: %.2f", gap)
	}
}

func TestBTRatingsTieSymmetric(t *testing.T) {
	r := btRatings([]btMatch{{a: "a", b: "b", outcome: 0.5}})
	if r["a"] != r["b"] {
		t.Fatalf("pure tie must rate equal, got %+v", r)
	}
	if r["a"] != btInitRating {
		t.Fatalf("symmetric pair must center at %v, got %+v", btInitRating, r)
	}
}

func TestBTRatingsSkipsBlindAndUnvoted(t *testing.T) {
	e := Eval{Rounds: []Round{
		{ModelA: "x", ModelB: "y", Sealed: true, Winner: VoteA}, // blind: excluded
		{ModelA: "x", ModelB: "y", Winner: ""},                  // unvoted: excluded
		{ModelA: "x", ModelB: "y", Winner: VoteTie},             // counted
		{ModelA: "", ModelB: "", Winner: VoteA},                 // unlabeled: excluded
	}}
	sc := ScoreOf(e)
	if len(sc.BT) != 2 || sc.BT["x"] != sc.BT["y"] {
		t.Fatalf("only the revealed tie must score, got %+v", sc.BT)
	}
}

func TestBTRatingsEmpty(t *testing.T) {
	if r := btRatings(nil); r != nil {
		t.Fatalf("empty input must give nil, got %+v", r)
	}
	if r := btRatings([]btMatch{{a: "", b: "", outcome: 1}}); r != nil {
		t.Fatalf("unlabeled input must give nil, got %+v", r)
	}
}
