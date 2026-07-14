// Package embedder is the gRPC client for the external embedder daemon.
// Spec: docs/v04/spec/components/embedder.md.
// Decision: D30 gRPC over Unix socket.
//
// rune-mcp does NOT spawn or manage the embedder. It connects as client.
// Socket path priority (spec/components/embedder.md §소켓 경로):
//  1. env RUNE_EMBEDDER_SOCKET
//  2. config.embedder.socket_path
//  3. embedder project convention default
//
// Retry policy (D7): [0, 500ms, 2s] × 3 on Unavailable / DeadlineExceeded /
// ResourceExhausted. Boot does NOT poll Health (D8) — first embed call drives.
package embedder

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	runedv1 "github.com/CryptoLabInc/runed/gen/runed/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// RetryBackoffs — D7 (Python server.py timeout equivalent).
var RetryBackoffs = []time.Duration{
	0,
	500 * time.Millisecond,
	2 * time.Second,
}

// InfoSnapshot — cached via sync.Once on first call.
type InfoSnapshot struct {
	DaemonVersion string
	ModelIdentity string
	VectorDim     int
	MaxTextLength int
	MaxBatchSize  int
	// CentroidSetVersion is runed's CURRENT routing set at snapshot time.
	// Unlike the fields above it is mutable (SetCentroids replaces it), so
	// a successful push invalidates the cache below.
	CentroidSetVersion string
}

// Status: OK / LOADING / DEGRADED / SHUTTING_DOWN
//
// Phase / BytesDone / BytesTotal / Message are populated by runed during
// LOADING
type HealthSnapshot struct {
	Status        string
	UptimeSeconds int64
	TotalRequests int64
	Phase         string // UNSPECIFIED | FETCHING_LLAMA_SERVER | FETCHING_MODEL | STARTING_LLAMA_SERVER
	BytesDone     int64
	BytesTotal    int64 // 0 when not downloading or total unknown
	Message       string
}

// Routed is an embedding plus its IVF cluster assignment (with_route).
type Routed struct {
	Vector             []float32
	ClusterID          uint32
	CentroidSetVersion string
}

// Client interface — thin wrapper over generated gRPC stub.
type Client interface {
	EmbedSingle(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	// EmbedRoute embeds and returns the cluster assignment. Fails with
	// FAILED_PRECONDITION if runed has no centroid set (push via SetCentroids).
	EmbedRoute(ctx context.Context, text string) (Routed, error)
	// SetCentroids pushes an IVF centroid set to runed for routing. preset is
	// the version-hash ingredient runed needs to verify the content hash
	// (empty = legacy chain, runed skips verification).
	SetCentroids(ctx context.Context, version string, dim int, preset string, vectors [][]float32) error
	Info(ctx context.Context) (InfoSnapshot, error)
	Health(ctx context.Context) (HealthSnapshot, error)
	SocketPath() string
	Close() error
}

type client struct {
	sockPath   string
	conn       *grpc.ClientConn
	pb         runedv1.RunedServiceClient
	info       *infoCache
	lastUptime atomic.Int64 // the most recent Health uptimes seconds.
	// (new < previous) indicates the daemon restarted
}

type Opts struct {
	UnaryInterceptors []grpc.UnaryClientInterceptor
}

// New dials the runed daemon over unix socket. The caller resolves sockPath
// (env RUNE_EMBEDDER_SOCKET > config.embedder.socket_path > default
// ~/.runed/embedding.sock per embedder.md §소켓 경로).
//
// grpc-go natively resolves "unix://" targets; no custom dialer is needed.
// TLS is unnecessary for UDS (kernel-mediated, same machine — embedder.md §Dial).
func New(sockPath string, opts Opts) (Client, error) {
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	if len(opts.UnaryInterceptors) > 0 {
		dialOpts = append(dialOpts, grpc.WithChainUnaryInterceptor(opts.UnaryInterceptors...))
	}
	conn, err := grpc.NewClient("unix://"+sockPath, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("embedder: grpc dial %s: %w", sockPath, err)
	}
	return newWithConn(sockPath, conn), nil
}

// NewBufconnClient wraps an existing *grpc.ClientConn (e.g., from
// google.golang.org/grpc/test/bufconn) so tests can exercise the same RPC
// path without needing a real unix socket / runed daemon.
func NewBufconnClient(conn *grpc.ClientConn) Client {
	return newWithConn("bufconn", conn)
}

func newWithConn(sockPath string, conn *grpc.ClientConn) *client {
	pb := runedv1.NewRunedServiceClient(conn)
	return &client{
		sockPath: sockPath,
		conn:     conn,
		pb:       pb,
		info:     &infoCache{svc: pb},
	}
}

// Hang guards. A wedged-but-listening runed (SIGSTOP, blocked disk I/O)
// answers nothing — the one failure class that produces no error, so every
// error path stays silent and the caller blocks forever (worst case: the boot
// relay, freezing the boot loop with no retry and no boot_error). An upper
// bound converts that silence into DeadlineExceeded, which the existing
// retry/surface machinery already handles. Applied only when the caller
// brought no deadline of its own; per attempt, so retries and the bootstrap
// wait each get a fresh budget. Vars so tests can shrink them.
var (
	EmbedCallTimeout    = 120 * time.Second // forward pass + idle-suspend wake + max batch
	CentroidPushTimeout = 60 * time.Second  // 16MB local stream + gob persist (measured <1s)
	ControlCallTimeout  = 10 * time.Second  // Info / Health probes
)

// withDefaultTimeout bounds ctx by d unless the caller already set a deadline.
func withDefaultTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

func (c *client) EmbedSingle(ctx context.Context, text string) ([]float32, error) {
	resp, err := retry(ctx, func(ctx context.Context) (*runedv1.EmbedResponse, error) {
		ctx, cancel := withDefaultTimeout(ctx, EmbedCallTimeout)
		defer cancel()
		return c.pb.Embed(ctx, &runedv1.EmbedRequest{Text: text})
	})
	if err != nil {
		return nil, err
	}
	return resp.GetVector(), nil
}

func (c *client) EmbedRoute(ctx context.Context, text string) (Routed, error) {
	resp, err := retry(ctx, func(ctx context.Context) (*runedv1.EmbedResponse, error) {
		ctx, cancel := withDefaultTimeout(ctx, EmbedCallTimeout)
		defer cancel()
		return c.pb.Embed(ctx, &runedv1.EmbedRequest{Text: text, WithRoute: true})
	})
	if err != nil {
		return Routed{}, err
	}
	return Routed{
		Vector:             resp.GetVector(),
		ClusterID:          resp.GetClusterId(),
		CentroidSetVersion: resp.GetCentroidSetVersion(),
	}, nil
}

// centroidPushBatch bounds one SetCentroids frame (64 x dim 1024 x 4B ~ 256KB).
const centroidPushBatch = 64

func (c *client) SetCentroids(ctx context.Context, version string, dim int, preset string, vectors [][]float32) error {
	ctx, cancel := withDefaultTimeout(ctx, CentroidPushTimeout)
	defer cancel()
	stream, err := c.pb.SetCentroids(ctx)
	if err != nil {
		return fmt.Errorf("embedder: set centroids open: %w", err)
	}
	if err := stream.Send(&runedv1.SetCentroidsRequest{Payload: &runedv1.SetCentroidsRequest_Header{
		Header: &runedv1.CentroidSetHeader{Version: version, Dim: uint32(dim), Nlist: uint32(len(vectors)), Preset: preset},
	}}); err != nil {
		return fmt.Errorf("embedder: set centroids header: %w", err)
	}
	for lo := 0; lo < len(vectors); lo += centroidPushBatch {
		hi := lo + centroidPushBatch
		if hi > len(vectors) {
			hi = len(vectors)
		}
		batch := make([]*runedv1.Centroid, 0, hi-lo)
		for i := lo; i < hi; i++ {
			batch = append(batch, &runedv1.Centroid{Id: uint32(i), Vec: vectors[i]})
		}
		if err := stream.Send(&runedv1.SetCentroidsRequest{Payload: &runedv1.SetCentroidsRequest_Batch{
			Batch: &runedv1.CentroidBatch{Centroids: batch},
		}}); err != nil {
			return fmt.Errorf("embedder: set centroids batch: %w", err)
		}
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		return fmt.Errorf("embedder: set centroids close: %w", err)
	}
	c.info.invalidate() // the push changed runed's CentroidSetVersion
	return nil
}

// EmbedBatch splits len(texts) > Info.MaxBatchSize into chunks and submits
// each chunk via embedBatchOnce. Order is preserved.
func (c *client) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	info, err := c.info.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("embedder: load Info before EmbedBatch: %w", err)
	}

	if info.MaxBatchSize <= 0 || len(texts) <= info.MaxBatchSize {
		return c.embedBatchOnce(ctx, texts)
	}

	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += info.MaxBatchSize {
		end := i + info.MaxBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		chunk, err := c.embedBatchOnce(ctx, texts[i:end])
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
	}
	return out, nil
}

func (c *client) embedBatchOnce(ctx context.Context, texts []string) ([][]float32, error) {
	resp, err := retry(ctx, func(ctx context.Context) (*runedv1.EmbedBatchResponse, error) {
		ctx, cancel := withDefaultTimeout(ctx, EmbedCallTimeout)
		defer cancel()
		return c.pb.EmbedBatch(ctx, &runedv1.EmbedBatchRequest{Texts: texts})
	})
	if err != nil {
		return nil, err
	}
	if len(resp.GetEmbeddings()) != len(texts) {
		return nil, fmt.Errorf("embedder: expected %d embeddings, got %d", len(texts), len(resp.GetEmbeddings()))
	}
	out := make([][]float32, len(resp.GetEmbeddings()))
	for i, e := range resp.GetEmbeddings() {
		out[i] = e.GetVector()
	}
	return out, nil
}

func (c *client) Info(ctx context.Context) (InfoSnapshot, error) {
	ctx, cancel := withDefaultTimeout(ctx, ControlCallTimeout)
	defer cancel()
	return c.info.Get(ctx)
}

func (c *client) SocketPath() string { return c.sockPath }

// Health issues a Health RPC. Status maps proto enum (STATUS_OK / STATUS_LOADING /
// STATUS_IDLE / STATUS_DEGRADED / STATUS_SHUTTING_DOWN / STATUS_UNSPECIFIED) to the
// "STATUS_"-stripped string the spec documents (OK / LOADING / IDLE / DEGRADED /
// SHUTTING_DOWN / UNSPECIFIED).
//
// Health is NOT retried — D8 says first embed call drives connectivity; Health
// is a diagnostic tool surface (console_status, diagnostics).
func (c *client) Health(ctx context.Context) (HealthSnapshot, error) {
	ctx, cancel := withDefaultTimeout(ctx, ControlCallTimeout)
	defer cancel()
	resp, err := c.pb.Health(ctx, &runedv1.HealthRequest{})
	if err != nil {
		return HealthSnapshot{}, MapGRPCError(err)
	}

	// Detect daemon restart and invalidate if restarted
	newUptime := resp.GetUptimeSeconds()
	if prev := c.lastUptime.Load(); prev > 0 && newUptime < prev {
		c.info.invalidate()
	}
	c.lastUptime.Store(newUptime)

	return HealthSnapshot{
		Status:        statusName(resp.GetStatus()),
		UptimeSeconds: newUptime,
		TotalRequests: resp.GetTotalRequests(),
		Phase:         phaseName(resp.GetPhase()),
		BytesDone:     resp.GetBytesDone(),
		BytesTotal:    resp.GetBytesTotal(),
		Message:       resp.GetMessage(),
	}, nil
}

func statusName(s runedv1.HealthResponse_Status) string {
	switch s {
	case runedv1.HealthResponse_STATUS_OK:
		return "OK"
	case runedv1.HealthResponse_STATUS_LOADING:
		return "LOADING"
	case runedv1.HealthResponse_STATUS_DEGRADED:
		return "DEGRADED"
	case runedv1.HealthResponse_STATUS_IDLE:
		return "IDLE"
	case runedv1.HealthResponse_STATUS_SHUTTING_DOWN:
		return "SHUTTING_DOWN"
	default:
		return "UNSPECIFIED"
	}
}

func phaseName(p runedv1.HealthResponse_Phase) string {
	switch p {
	case runedv1.HealthResponse_PHASE_FETCHING_LLAMA_SERVER:
		return "FETCHING_LLAMA_SERVER"
	case runedv1.HealthResponse_PHASE_FETCHING_MODEL:
		return "FETCHING_MODEL"
	case runedv1.HealthResponse_PHASE_STARTING_LLAMA_SERVER:
		return "STARTING_LLAMA_SERVER"
	default:
		return "UNSPECIFIED"
	}
}
func (c *client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
