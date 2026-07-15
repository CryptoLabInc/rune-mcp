package console

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	consolepb "github.com/CryptoLabInc/rune-console/pkg/consolepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// BootstrapTimeout bounds each bootstrap RPC (CA fetch, unwrap).
const BootstrapTimeout = 15 * time.Second

// FetchCACert is stage 1 of the 3-stage connection bootstrap. It dials endpoint
// over an UNTRUSTED channel — TLS with certificate verification disabled, since
// the client has no CA yet — calls GetCACert, and returns the served CA PEM
// only after its SHA-256 matches expectedSHA256 (lowercase hex). The pin,
// carried out-of-band in the registration string, is the sole trust anchor for
// this step; the transport being unverified is intentional and safe because a
// tampered CA fails the pin check.
//
// expectedSHA256 == "" selects the plaintext dev path: the endpoint is dialed
// without TLS and the returned PEM (if any) is not pin-checked.
func FetchCACert(ctx context.Context, endpoint, expectedSHA256 string) ([]byte, error) {
	target, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return nil, fmt.Errorf("console bootstrap: %w", err)
	}

	var creds credentials.TransportCredentials
	if expectedSHA256 == "" {
		creds = insecure.NewCredentials()
	} else {
		// #nosec G402 — trust is anchored by the SHA-256 pin verified below,
		// not by the certificate chain (the client has no CA yet).
		creds = credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})
	}

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("console bootstrap: dial %s: %w", target, err)
	}
	defer func() { _ = conn.Close() }()

	cctx, cancel := context.WithTimeout(ctx, BootstrapTimeout)
	defer cancel()
	resp, err := consolepb.NewConsoleServiceClient(conn).GetCACert(cctx, &consolepb.GetCACertRequest{})
	if err != nil {
		return nil, MapGRPCError(err)
	}
	if msg := resp.GetError(); msg != "" {
		return nil, &Error{Code: ErrConsoleInternal.Code, Message: "GetCACert: " + msg}
	}
	pem := resp.GetCaPem()
	if len(pem) == 0 {
		return nil, &Error{Code: "CONSOLE_BOOTSTRAP", Message: "GetCACert returned an empty CA"}
	}
	if expectedSHA256 != "" {
		sum := sha256.Sum256(pem)
		got := hex.EncodeToString(sum[:])
		if !strings.EqualFold(got, expectedSHA256) {
			return nil, &Error{
				Code:    "CONSOLE_CA_PIN_MISMATCH",
				Message: fmt.Sprintf("CA pin mismatch: served %s, expected %s", got, expectedSHA256),
			}
		}
	}
	return pem, nil
}

// Unwrap is stage 2: it dials endpoint (verifying the console's TLS against
// caPEM when non-empty, else plaintext) and redeems the one-time wrapping
// handle for the real access token. The handle is single-use — a second call
// fails with "already used", which is the tamper signal for the caller to
// rotate rather than proceed.
func Unwrap(ctx context.Context, endpoint string, caPEM []byte, handle string) (string, error) {
	target, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return "", fmt.Errorf("console bootstrap: %w", err)
	}

	var creds credentials.TransportCredentials
	if len(caPEM) == 0 {
		creds = insecure.NewCredentials()
	} else {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return "", &Error{Code: "CONSOLE_BOOTSTRAP", Message: "pinned CA is not valid PEM"}
		}
		creds = credentials.NewTLS(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	}

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return "", fmt.Errorf("console bootstrap: dial %s: %w", target, err)
	}
	defer func() { _ = conn.Close() }()

	cctx, cancel := context.WithTimeout(ctx, BootstrapTimeout)
	defer cancel()
	resp, err := consolepb.NewConsoleServiceClient(conn).Unwrap(cctx, &consolepb.UnwrapRequest{Handle: handle})
	if err != nil {
		return "", MapGRPCError(err)
	}
	if msg := resp.GetError(); msg != "" {
		return "", &Error{Code: "CONSOLE_UNWRAP", Message: "Unwrap: " + msg}
	}
	if resp.GetToken() == "" {
		return "", &Error{Code: "CONSOLE_UNWRAP", Message: "Unwrap returned an empty token"}
	}
	return resp.GetToken(), nil
}
