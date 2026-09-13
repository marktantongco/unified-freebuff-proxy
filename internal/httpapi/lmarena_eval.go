package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"

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
