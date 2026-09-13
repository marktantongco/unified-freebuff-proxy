package eval

import "math"

// Bradley-Terry ratings following the arena-rank methodology
// (github.com/lmarena/arena-rank, Apache-2.0):
//   - outcomes: win = 1, tie = 0.5 each side, loss = 0
//   - P(A beats B) = sigmoid(theta_a - theta_b)
//   - display rating R = 1000 + alpha*(theta - mean(theta)),
//     alpha = 400/ln(10) (standard Elo-400 scale)
//
// Fit uses Hunter's MM algorithm (deterministic, dependency-free), which
// converges to the same weighted MLE as arena-rank's L-BFGS fit on the
// same sufficient statistics. Iteration count is bounded; small harness
// data converges in tens of iterations.

const (
	btInitRating = 1000.0
	btScale      = 400.0
	btMaxIter    = 1000
	btTol        = 1e-9
	// btTiePrior is a weak virtual-tie mass per model against an average
	// opponent. Sparse harness data (e.g. one 2-0 record) makes the plain
	// MLE diverge to +-infinity — arena-rank's unregularized L-BFGS shares
	// this flaw on thin data. The prior bounds extremes, pulls toward the
	// mean, and vanishes as real votes accumulate (same limit as the MLE).
	btTiePrior = 1.0
)

// btAlpha converts theta-space gaps to Elo-scale rating gaps.
var btAlpha = btScale / math.Log(10)

// btMatch is one revealed voted round between two labeled models.
type btMatch struct {
	a, b    string
	outcome float64 // P(a beats b): 1 = a wins, 0.5 = tie, 0 = b wins
}

// btRatings fits Bradley-Terry strengths and returns Elo-scale ratings
// keyed by model. Ties split half/half, matching arena-rank's outcome map.
// Models with no opponents (or empty input) are omitted; a single model
// rates exactly btInitRating.
func btRatings(matches []btMatch) map[string]float64 {
	// Aggregate sufficient statistics: wins per model, games per pair.
	wins := map[string]float64{}
	pairGames := map[[2]string]float64{}
	models := map[string]bool{}
	addGame := func(x, y string) {
		if x == y {
			return
		}
		if x > y {
			x, y = y, x
		}
		pairGames[[2]string{x, y}]++
	}
	for _, m := range matches {
		if m.a == "" || m.b == "" || m.a == m.b {
			continue
		}
		models[m.a] = true
		models[m.b] = true
		wins[m.a] += m.outcome
		wins[m.b] += 1 - m.outcome
		addGame(m.a, m.b)
	}
	if len(models) == 0 {
		return nil
	}
	// Strengths in theta space (pi = exp(theta)), init zero = equal.
	// Each model also carries btTiePrior virtual ties vs an average
	// opponent (pi = 1): sparse-data regularization, see const comment.
	pi := map[string]float64{}
	for m := range models {
		pi[m] = 1.0
		wins[m] += btTiePrior * 0.5
	}
	for range btMaxIter {
		maxChange := 0.0
		next := map[string]float64{}
		for m := range models {
			denom := 0.0
			for p, n := range pairGames {
				var opp string
				if p[0] == m {
					opp = p[1]
				} else if p[1] == m {
					opp = p[0]
				} else {
					continue
				}
				denom += n / (pi[m] + pi[opp])
			}
			if denom == 0 {
				next[m] = pi[m]
				continue
			}
			// Virtual-tie opponent has pi = 1 by definition (average).
			next[m] = wins[m] / (denom + btTiePrior/(pi[m]+1))
		}
		// Normalize (geometric mean 1) to fix the scale degree of freedom.
		logSum := 0.0
		for m := range models {
			if next[m] <= 0 {
				next[m] = 1e-12
			}
			logSum += math.Log(next[m])
		}
		mean := logSum / float64(len(models))
		for m := range models {
			next[m] /= math.Exp(mean)
			if d := math.Abs(next[m] - pi[m]); d > maxChange {
				maxChange = d
			}
		}
		pi = next
		if maxChange < btTol {
			break
		}
	}
	// Zero-centered Elo display ratings, rounded to 2dp.
	out := make(map[string]float64, len(models))
	for m, p := range pi {
		out[m] = math.Round((btInitRating+btAlpha*math.Log(p))*100) / 100
	}
	return out
}

// btMatchesOf extracts revealed voted rounds as BT matches. Sealed rounds
// never contribute: blind integrity comes before rating coverage.
func btMatchesOf(e Eval) []btMatch {
	var out []btMatch
	for _, r := range e.Rounds {
		if r.Sealed || r.ModelA == "" || r.ModelB == "" {
			continue
		}
		var outcome float64
		switch r.Winner {
		case VoteA:
			outcome = 1.0
		case VoteB:
			outcome = 0.0
		case VoteTie:
			outcome = 0.5
		default:
			continue
		}
		out = append(out, btMatch{a: r.ModelA, b: r.ModelB, outcome: outcome})
	}
	return out
}
