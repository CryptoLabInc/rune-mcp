// Package console is the Rune-console gRPC client.
//
// Under the runespace model the console holds ALL FHE keys and is the sole
// runespace client. rune-mcp is a pure-Go client that only talks to this
// service — it never encrypts/decrypts or touches runespace directly.
//
// Responsibility:
//   - GetAgentManifest: fetch agent config (no keys)
//   - Insert: send a plaintext embedding + metadata; console encrypts + seals + stores
//   - Search: send a plaintext query; console searches + decrypts + opens metadata
package console

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	consolepb "github.com/CryptoLabInc/rune-console/pkg/consolepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// MaxMessageLength — 256MB on both send/recv (FHE ciphertexts are large, and
// the console relays plaintext vectors which are small; keep the generous cap).
const MaxMessageLength = 256 * 1024 * 1024

// DefaultTimeout — per-RPC deadline.
const DefaultTimeout = 30 * time.Second

// HealthCheckTimeout — override on health probes.
const HealthCheckTimeout = 5 * time.Second

// Bundle is the agent manifest returned by GetAgentManifest. Under the
// client-side-crypto model it carries the PUBLIC EncKey pair and the caller's
// derived agent_dek so rune-mcp can encrypt/seal locally; SecKey and
// team_secret stay in the Console.
type Bundle struct {
	AgentID string
	KeyID   string
	Dim     int

	AuthorEmail       string // record attribution
	AuthorDisplayName string

	RMPEncKey          []byte // RMP EncKey envelope (verbatim JSON)
	MMEncKey           []byte // MM EncKey envelope (verbatim JSON)
	AgentDEK           []byte // metadata seal key (base64-decoded)
	CentroidSetVersion string // engine's current set; "" = none loaded yet
}

type manifestJSON struct {
	AgentID            string `json:"agent_id"`
	KeyID              string `json:"key_id"`
	Dim                int    `json:"dim"`
	AuthorEmail        string `json:"author_email"`
	AuthorDisplayName  string `json:"author_display_name"`
	RMPEncKey          string `json:"rmp_enc_key"`
	MMEncKey           string `json:"mm_enc_key"`
	AgentDEK           string `json:"agent_dek"`
	CentroidSetVersion string `json:"centroid_set_version"`
}

func ParseManifestJSON(raw string) (*Bundle, error) {
	var m manifestJSON
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("console: parse manifest_json: %w", err)
	}
	b := &Bundle{
		AgentID:            m.AgentID,
		KeyID:              m.KeyID,
		Dim:                m.Dim,
		AuthorEmail:        m.AuthorEmail,
		AuthorDisplayName:  m.AuthorDisplayName,
		RMPEncKey:          []byte(m.RMPEncKey),
		MMEncKey:           []byte(m.MMEncKey),
		CentroidSetVersion: m.CentroidSetVersion,
	}
	if m.AgentDEK != "" {
		dek, err := base64.StdEncoding.DecodeString(m.AgentDEK)
		if err != nil {
			return nil, fmt.Errorf("console: decode agent_dek: %w", err)
		}
		b.AgentDEK = dek
	}
	return b, nil
}

// Hit is one decrypted, ranked search result. Metadata is plaintext JSON
// (the console opened the sealed envelope).
type Hit struct {
	ID       string
	Score    float64
	Metadata string
}

// InsertItem is a client-encrypted capture item forwarded verbatim to
// runespace via the Console. ID is client-generated so retries are idempotent.
type InsertItem struct {
	ID                 string
	RMPItem            []byte // EncryptFlat output
	MMItem             []byte // EncryptClustered output
	ClusterID          uint32
	CentroidSetVersion string
	SealedMetadata     string // client-sealed {"a","c"} envelope
}

// CentroidSet is the relayed IVF centroid set (runespace -> console -> here).
// Preset is a version-hash ingredient — relayed through to runed so it can
// recompute and verify the content hash.
type CentroidSet struct {
	Version string
	Dim     int
	Preset  string
	Vectors [][]float32
}

// Client interface — implemented by gRPC client (and test mocks).
type Client interface {
	GetAgentManifest(ctx context.Context) (*Bundle, error)
	Insert(ctx context.Context, item InsertItem) (string, error)
	Search(ctx context.Context, vector []float32, topK int) ([]Hit, error)
	Centroids(ctx context.Context) (*CentroidSet, error)
	HealthCheck(ctx context.Context) (bool, error)
	Endpoint() string
	Close() error
}

type ClientOpts struct {
	CACertPath        string // path to PEM; empty = system CA bundle
	UnaryInterceptors []grpc.UnaryClientInterceptor
}

// client is the gRPC implementation.
type client struct {
	endpoint string
	token    string
	conn     *grpc.ClientConn
	stub     consolepb.ConsoleServiceClient
}

var defaultKeepalive = keepalive.ClientParameters{
	Time:                30 * time.Second,
	Timeout:             10 * time.Second,
	PermitWithoutStream: true,
}

func NewClient(endpoint, token string, opts ClientOpts) (Client, error) {
	normalized, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return nil, fmt.Errorf("console: invalid endpoint: %w", err)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(MaxMessageLength),
			grpc.MaxCallSendMsgSize(MaxMessageLength),
		),
		grpc.WithKeepaliveParams(defaultKeepalive),
	}
	if len(opts.UnaryInterceptors) > 0 {
		dialOpts = append(dialOpts, grpc.WithChainUnaryInterceptor(opts.UnaryInterceptors...))
	}

	if opts.CACertPath != "" {
		creds, err := credentials.NewClientTLSFromFile(opts.CACertPath, "")
		if err != nil {
			return nil, fmt.Errorf("console: failed to load CA cert %s: %w", opts.CACertPath, err)
		}
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	}

	conn, err := grpc.NewClient(normalized, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("console: grpc dial failed: %w", err)
	}

	slog.Info("console: connected", "endpoint", normalized)
	return newWithConn(normalized, token, conn), nil
}

// NewBufconnClient wraps an existing *grpc.ClientConn for tests.
func NewBufconnClient(conn *grpc.ClientConn, token string) Client {
	return newWithConn("bufconn", token, conn)
}

func newWithConn(endpoint, token string, conn *grpc.ClientConn) *client {
	return &client{
		endpoint: endpoint,
		token:    token,
		conn:     conn,
		stub:     consolepb.NewConsoleServiceClient(conn),
	}
}

func (c *client) authCtx(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.token)
}

func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= d {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

func (c *client) GetAgentManifest(ctx context.Context) (*Bundle, error) {
	ctx, cancel := withTimeout(c.authCtx(ctx), DefaultTimeout)
	defer cancel()

	resp, err := c.stub.GetAgentManifest(ctx, &consolepb.GetAgentManifestRequest{Token: c.token})
	if err != nil {
		return nil, MapGRPCError(err)
	}
	if msg := resp.GetError(); msg != "" {
		return nil, &Error{Code: ErrConsoleInternal.Code, Message: "GetAgentManifest: " + msg, Retryable: true}
	}
	return ParseManifestJSON(resp.GetManifestJson())
}

func (c *client) Insert(ctx context.Context, item InsertItem) (string, error) {
	ctx, cancel := withTimeout(c.authCtx(ctx), DefaultTimeout)
	defer cancel()

	resp, err := c.stub.Insert(ctx, &consolepb.InsertRequest{
		Token:              c.token,
		Id:                 item.ID,
		RmpItem:            item.RMPItem,
		MmItem:             item.MMItem,
		ClusterId:          item.ClusterID,
		CentroidSetVersion: item.CentroidSetVersion,
		Metadata:           item.SealedMetadata,
	})
	if err != nil {
		return "", MapGRPCError(err)
	}
	if msg := resp.GetError(); msg != "" {
		return "", &Error{Code: ErrConsoleInternal.Code, Message: "Insert: " + msg, Retryable: true}
	}
	return resp.GetId(), nil
}

// Centroids pulls the relayed IVF centroid set (header + id-ordered batches).
func (c *client) Centroids(ctx context.Context) (*CentroidSet, error) {
	ctx, cancel := withTimeout(c.authCtx(ctx), DefaultTimeout)
	defer cancel()

	stream, err := c.stub.GetCentroids(ctx, &consolepb.GetCentroidsRequest{Token: c.token})
	if err != nil {
		return nil, MapGRPCError(err)
	}
	cs := &CentroidSet{}
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, MapGRPCError(err)
		}
		switch p := chunk.GetPayload().(type) {
		case *consolepb.CentroidChunk_Header:
			cs.Version = p.Header.GetVersion()
			cs.Dim = int(p.Header.GetDim())
			cs.Preset = p.Header.GetPreset()
			if n := p.Header.GetNlist(); n > 0 {
				cs.Vectors = make([][]float32, 0, n)
			}
		case *consolepb.CentroidChunk_Batch:
			for _, ct := range p.Batch.GetCentroids() {
				cs.Vectors = append(cs.Vectors, ct.GetVec())
			}
		}
	}
	return cs, nil
}

func (c *client) Search(ctx context.Context, vector []float32, topK int) ([]Hit, error) {
	ctx, cancel := withTimeout(c.authCtx(ctx), DefaultTimeout)
	defer cancel()

	resp, err := c.stub.Search(ctx, &consolepb.SearchRequest{
		Token:  c.token,
		Vector: vector,
		TopK:   int32(topK),
	})
	if err != nil {
		return nil, MapGRPCError(err)
	}
	if msg := resp.GetError(); msg != "" {
		return nil, &Error{Code: ErrConsoleInternal.Code, Message: "Search: " + msg, Retryable: true}
	}
	out := make([]Hit, 0, len(resp.GetHits()))
	for _, h := range resp.GetHits() {
		out = append(out, Hit{ID: h.GetId(), Score: h.GetScore(), Metadata: h.GetMetadata()})
	}
	return out, nil
}

func (c *client) HealthCheck(ctx context.Context) (bool, error) {
	ctx, cancel := withTimeout(ctx, HealthCheckTimeout)
	defer cancel()

	stub := grpc_health_v1.NewHealthClient(c.conn)
	resp, err := stub.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: ""})
	if err != nil {
		return false, MapGRPCError(err)
	}
	return resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING, nil
}

func (c *client) Endpoint() string { return c.endpoint }

func (c *client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
