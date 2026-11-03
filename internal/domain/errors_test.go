package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestMakeErrorKeepsWrappedDomainContract(t *testing.T) {
	err := fmt.Errorf("console insert: %w", ErrConsoleUpgradeRequired)
	got := MakeError(err)
	body := got["error"].(map[string]any)
	if got["ok"] != false || body["code"] != CodeConsoleUpgradeRequired || body["message"] != ErrConsoleUpgradeRequired.Message || body["retryable"] != false || body["recovery_hint"] != ConsoleUpgradeRecoveryHint {
		t.Fatalf("wrapped response: %#v", got)
	}
}
func TestMakeErrorUnknownIsPermanentInternal(t *testing.T) {
	got := MakeError(errors.New("unexpected adapter failure"))
	body := got["error"].(map[string]any)
	if got["ok"] != false || body["code"] != CodeInternal || body["retryable"] != false || body["message"] != "unexpected adapter failure" {
		t.Fatalf("unknown response: %#v", got)
	}
}
