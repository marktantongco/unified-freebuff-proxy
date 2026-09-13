package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"freebuff-unified/internal/eval"
	"github.com/gofiber/fiber/v3"
)

// Manual-eval harness: a human drives lmarena.ai by hand in their own
// browser, pastes the two outputs here, and the gateway only stores, blinds
// and scores. No upstream fetch happens on this path by design.

func decodeEvalJSON(c fiber.Ctx, dst any) bool {
	dec := json.NewDecoder(bytes.NewReader(c.Body()))
	if err := dec.Decode(dst); err != nil {
		return false
	}
	return true
}

// EvalCreate starts a new empty eval. Body: {"name": "..."}.
func (h *handlers) EvalCreate(c fiber.Ctx) error {
	if h.evals == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(errBody("evals_disabled", "eval harness is not enabled"))
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeEvalJSON(c, &req) {
		return c.Status(http.StatusBadRequest).JSON(errBody("invalid_request", "body must be JSON with a name"))
	}
	e, err := h.evals.Create(req.Name)
	if err != nil {
		return c.Status(http.StatusBadRequest).JSON(errBody("invalid_request", err.Error()))
	}
	return c.Status(http.StatusCreated).JSON(evalView(e))
}

// EvalList lists evals (rounds omitted).
func (h *handlers) EvalList(c fiber.Ctx) error {
	if h.evals == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(errBody("evals_disabled", "eval harness is not enabled"))
	}
	return c.Status(http.StatusOK).JSON(map[string]any{"evals": h.evals.List()})
}

// EvalGet returns one eval with blinded rounds plus the score.
func (h *handlers) EvalGet(c fiber.Ctx) error {
	if h.evals == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(errBody("evals_disabled", "eval harness is not enabled"))
	}
	e, ok := h.evals.Get(c.Params("id"))
	if !ok {
		return c.Status(http.StatusNotFound).JSON(errBody("eval_not_found", "eval not found"))
	}
	return c.Status(http.StatusOK).JSON(evalView(e))
}

// EvalAddRound pastes one blind round. Body: {"prompt", "output_a",
// "output_b", optional "model_a"/"model_b" sealed until reveal}.
func (h *handlers) EvalAddRound(c fiber.Ctx) error {
	if h.evals == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(errBody("evals_disabled", "eval harness is not enabled"))
	}
	var req struct {
		Prompt  string `json:"prompt"`
		OutputA string `json:"output_a"`
		OutputB string `json:"output_b"`
		ModelA  string `json:"model_a"`
		ModelB  string `json:"model_b"`
	}
	if !decodeEvalJSON(c, &req) {
		return c.Status(http.StatusBadRequest).JSON(errBody("invalid_request", "body must be JSON with prompt, output_a, output_b"))
	}
	r, err := h.evals.AddRound(c.Params("id"), req.Prompt, req.OutputA, req.OutputB, req.ModelA, req.ModelB)
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "eval not found" {
			status = http.StatusNotFound
		}
		return c.Status(status).JSON(errBody("invalid_request", err.Error()))
	}
	return c.Status(http.StatusCreated).JSON(blindRound(r))
}

// EvalVote records the winner of a round once. Body: {"winner": "a"|"b"|"tie"}.
func (h *handlers) EvalVote(c fiber.Ctx) error {
	if h.evals == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(errBody("evals_disabled", "eval harness is not enabled"))
	}
	var req struct {
		Winner string `json:"winner"`
	}
	if !decodeEvalJSON(c, &req) {
		return c.Status(http.StatusBadRequest).JSON(errBody("invalid_request", `body must be JSON with winner "a", "b" or "tie"`))
	}
	r, err := h.evals.RecordVote(c.Params("id"), c.Params("rid"), req.Winner)
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "eval not found" || err.Error() == "round not found" {
			status = http.StatusNotFound
		}
		return c.Status(status).JSON(errBody("invalid_request", err.Error()))
	}
	return c.Status(http.StatusOK).JSON(blindRound(r))
}

// EvalImport bulk-pastes rounds. Body: {"format": "jsonl"|"csv", "data":
// "<raw records>"}. Rounds with model labels seal; records carrying a
// winner arrive pre-voted. Bounded to eval.maxImportRecords per call.
func (h *handlers) EvalImport(c fiber.Ctx) error {
	if h.evals == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(errBody("evals_disabled", "eval harness is not enabled"))
	}
	var req struct {
		Format string `json:"format"`
		Data   string `json:"data"`
	}
	if !decodeEvalJSON(c, &req) {
		return c.Status(http.StatusBadRequest).JSON(errBody("invalid_request", `body must be JSON with format "jsonl"|"csv" and data`))
	}
	var (
		recs []eval.ImportRecord
		err  error
	)
	switch req.Format {
	case "jsonl":
		recs, err = eval.ParseJSONL(req.Data)
	case "csv":
		recs, err = eval.ParseCSV(req.Data)
	default:
		return c.Status(http.StatusBadRequest).JSON(errBody("invalid_request", `format must be "jsonl" or "csv"`))
	}
	if err != nil {
		return c.Status(http.StatusBadRequest).JSON(errBody("invalid_request", err.Error()))
	}
	rounds, err := h.evals.Import(c.Params("id"), recs)
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "eval not found" {
			status = http.StatusNotFound
		}
		return c.Status(status).JSON(errBody("invalid_request", err.Error()))
	}
	ids := make([]string, len(rounds))
	for i, r := range rounds {
		ids[i] = r.ID
	}
	return c.Status(http.StatusCreated).JSON(map[string]any{"imported": len(rounds), "round_ids": ids})
}

// Leaderboard serves the public text-leaderboard snapshot
// (GET /v1/lmarena/leaderboard?category=overall&top=5). Read-only HF data,
// cached 24h by default; stale cache covers HF downtime. The fetch runs on
// a detached context: client disconnect must not abort a 100-page refresh
// other requests may be waiting on.
func (h *handlers) Leaderboard(c fiber.Ctx) error {
	if h.board == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(errBody("leaderboard_disabled", "leaderboard snapshot is not enabled"))
	}
	category := c.Query("category", "overall")
	top := 0
	if raw := c.Query("top", ""); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return c.Status(http.StatusBadRequest).JSON(errBody("invalid_request", "top must be a positive integer"))
		}
		if n > 200 {
			n = 200
		}
		top = n
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), 3*time.Minute)
	defer cancel()
	snap, err := h.board.Get(ctx, category)
	if err != nil {
		return c.Status(http.StatusBadGateway).JSON(errBody("leaderboard_unavailable", err.Error()))
	}
	if top > 0 && len(snap.Entries) > top {
		snap.Entries = snap.Entries[:top]
	}
	return c.Status(http.StatusOK).JSON(snap)
}

// EvalReveal unseals model labels and returns the full eval plus score.
func (h *handlers) EvalReveal(c fiber.Ctx) error {
	if h.evals == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(errBody("evals_disabled", "eval harness is not enabled"))
	}
	e, err := h.evals.Reveal(c.Params("id"))
	if err != nil {
		return c.Status(http.StatusNotFound).JSON(errBody("eval_not_found", err.Error()))
	}
	return c.Status(http.StatusOK).JSON(evalView(e))
}

func evalView(e eval.Eval) map[string]any {
	return map[string]any{"eval": eval.Blind(e), "score": eval.ScoreOf(e)}
}

func blindRound(r eval.Round) map[string]any {
	if r.Sealed {
		r.ModelA = ""
		r.ModelB = ""
	}
	return map[string]any{"round": r}
}

func errBody(code, message string) map[string]any {
	return map[string]any{"error": map[string]any{"message": message, "type": code}}
}
