package embedder_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	runedv1 "github.com/CryptoLabInc/runed/gen/runed/v1"
)

// SetCentroids delegates to the per-test fn (nil → Unimplemented default).
func (f *fakeRuned) SetCentroids(stream runedv1.RunedService_SetCentroidsServer) error {
	if f.setCentroidsFn != nil {
		return f.setCentroidsFn(stream)
	}
	return f.UnimplementedRunedServiceServer.SetCentroids(stream)
}

func shrinkTimeouts(t *testing.T, embed, push time.Duration) {
	t.Helper()
	pe, pp := embedder.EmbedCallTimeout, embedder.CentroidPushTimeout
	embedder.EmbedCallTimeout, embedder.CentroidPushTimeout = embed, push
	t.Cleanup(func() { embedder.EmbedCallTimeout, embedder.CentroidPushTimeout = pe, pp })
}

// A wedged runed answers nothing. The hang guard must convert that silence
// into a bounded, retryable error instead of blocking the caller forever —
// this is the one failure class no error path can otherwise see.
func TestEmbed_HangIsBounded(t *testing.T) {
	shrinkTimeouts(t, 40*time.Millisecond, time.Second)
	fake, cl := startFakeRuned(t)
	fake.embedFn = func(*runedv1.EmbedRequest) (*runedv1.EmbedResponse, error) {
		time.Sleep(2 * time.Second) // 응답 없는 좀비 흉내
		return &runedv1.EmbedResponse{}, nil
	}
	start := time.Now()
	_, err := cl.EmbedSingle(context.Background(), "x")
	if err == nil {
		t.Fatal("want bounded error from a hanging runed, got success")
	}
	var e *embedder.Error
	if !errors.As(err, &e) || !e.Retryable {
		t.Fatalf("want retryable embedder.Error, got %v", err)
	}
	// D7 3시도 × 40ms + 백오프(0/500ms/2s) < 4s — 영원한 블로킹이 아님을 확인
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("hang not bounded: %v", elapsed)
	}
}

func TestSetCentroids_HangIsBounded(t *testing.T) {
	shrinkTimeouts(t, time.Second, 40*time.Millisecond)
	fake, cl := startFakeRuned(t)
	fake.setCentroidsFn = func(stream runedv1.RunedService_SetCentroidsServer) error {
		<-stream.Context().Done() // ack를 영원히 안 보내는 좀비
		return stream.Context().Err()
	}
	start := time.Now()
	err := cl.SetCentroids(context.Background(), "v1", 2, "IP1", [][]float32{{1, 0}})
	if err == nil {
		t.Fatal("want bounded error, got success")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("hang not bounded: %v", elapsed)
	}
}

// 호출자가 자기 데드라인을 가져오면 기본 상한이 덮어쓰지 않는다.
func TestCallerDeadlineWins(t *testing.T) {
	shrinkTimeouts(t, 10*time.Millisecond, 10*time.Millisecond)
	fake, cl := startFakeRuned(t)
	fake.embedFn = func(*runedv1.EmbedRequest) (*runedv1.EmbedResponse, error) {
		time.Sleep(50 * time.Millisecond) // 기본 상한(10ms)보다 길고 호출자 상한(1s)보다 짧게
		return &runedv1.EmbedResponse{Vector: []float32{1}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := cl.EmbedSingle(ctx, "x"); err != nil {
		t.Fatalf("caller deadline should win over the shrunk default: %v", err)
	}
}
