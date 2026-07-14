package domain

// RuneError — 8-code taxonomy (Python 7 + Go-specific EMBEDDER_UNREACHABLE).
// Spec: docs/v04/spec/components/rune-mcp.md §에러 처리.
// Python: mcp/server/errors.py (118 LoC).
//
// The two vector-backend codes are RUNESPACE_* per the design contract (§9);
// they were ENVECTOR_* in the enVector-era Python server before mcp's direct
// link to the vector engine was removed (mcp now reaches it only via the console).

import "errors"

// Code enum — 8 codes.
const (
	CodeInternal            = "INTERNAL_ERROR"
	CodeConsoleConnection   = "CONSOLE_CONNECTION_ERROR"
	CodeConsoleDecryption   = "CONSOLE_DECRYPTION_ERROR"
	CodeRunespaceConnection = "RUNESPACE_CONNECTION_ERROR"
	CodeRunespaceInsert     = "RUNESPACE_INSERT_ERROR"
	CodePipelineNotReady    = "PIPELINE_NOT_READY"
	CodeInvalidInput        = "INVALID_INPUT"
	CodeTopKLimit           = "TOPK_LIMIT"           // top_k exceeds the console token's role limit (distinct from generic INVALID_INPUT)
	CodeEmbedderUnreachable = "EMBEDDER_UNREACHABLE" // Go-specific (D30)
	CodeEmptyEmbedText      = "EMPTY_EMBED_TEXT"     // D5 — dedicated code for missing embed text
	CodeExtractionMissing   = "EXTRACTION_MISSING"   // D14 — agent must provide pre_extraction
)

// RuneError — MCP error response body (Python make_error equivalent).
type RuneError struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Retryable    bool   `json:"retryable"`
	RecoveryHint string `json:"recovery_hint,omitempty"`
}

func (e *RuneError) Error() string { return e.Message }

// Predefined errors (Python errors.py equivalents).
var (
	ErrInternal            = &RuneError{Code: CodeInternal, Retryable: false}
	ErrConsoleConnection   = &RuneError{Code: CodeConsoleConnection, Retryable: true}
	ErrConsoleDecryption   = &RuneError{Code: CodeConsoleDecryption, Retryable: false}
	ErrRunespaceConnection = &RuneError{Code: CodeRunespaceConnection, Retryable: true}
	ErrRunespaceInsert     = &RuneError{Code: CodeRunespaceInsert, Retryable: true}
	ErrPipelineNotReady    = &RuneError{Code: CodePipelineNotReady, Retryable: false}
	ErrInvalidInput        = &RuneError{Code: CodeInvalidInput, Retryable: false}
	ErrTopKLimit           = &RuneError{Code: CodeTopKLimit, Retryable: false}
	ErrEmbedderUnreachable = &RuneError{Code: CodeEmbedderUnreachable, Retryable: true}
	ErrEmptyEmbedText      = &RuneError{Code: CodeEmptyEmbedText, Retryable: false}
	ErrExtractionMissing   = &RuneError{Code: CodeExtractionMissing, Retryable: false}
)

// MakeError — Python make_error equivalent. Wraps an error as MCP response.
func MakeError(err error) map[string]any {
	var runeErr *RuneError
	if errors.As(err, &runeErr) {
		errMap := map[string]any{
			"code":      runeErr.Code,
			"message":   runeErr.Message,
			"retryable": runeErr.Retryable,
		}
		if runeErr.RecoveryHint != "" {
			errMap["recovery_hint"] = runeErr.RecoveryHint
		}
		return map[string]any{
			"ok":    false,
			"error": errMap,
		}
	}
	return map[string]any{
		"ok": false,
		"error": map[string]any{
			"code":      CodeInternal,
			"message":   err.Error(),
			"retryable": false,
		},
	}
}
