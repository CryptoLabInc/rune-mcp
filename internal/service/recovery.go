package service

import (
	"context"
	"errors"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/vault"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
)

// These helpers replicate the transport interceptor: on a retryable vault
// failure, wait for the pipeline to become active again, then retry once.

func insertWithRecovery(ctx context.Context, state *lifecycle.Manager, c vault.Client, item vault.InsertItem) (string, error) {
	id, err := c.Insert(ctx, item)
	if err == nil {
		return id, nil
	}
	if state == nil || !isVaultRetryable(err) {
		return "", err
	}
	if !state.WaitForActive(ctx, lifecycle.RecoverTimeout) {
		return "", err
	}
	// Same item (same ID) — the forward is idempotent on retry.
	return c.Insert(ctx, item)
}

func searchWithRecovery(ctx context.Context, state *lifecycle.Manager, c vault.Client, vec []float32, topK int) ([]vault.Hit, error) {
	hits, err := c.Search(ctx, vec, topK)
	if err == nil {
		return hits, nil
	}
	if state == nil || !isVaultRetryable(err) {
		return nil, err
	}
	if !state.WaitForActive(ctx, lifecycle.RecoverTimeout) {
		return nil, err
	}
	return c.Search(ctx, vec, topK)
}

func isVaultRetryable(err error) bool {
	var e *vault.Error
	return errors.As(err, &e) && e.Retryable
}
