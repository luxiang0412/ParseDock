package coordinator

import (
	"context"
	"errors"
	"net/netip"
	"strings"
)

// clientIPHeader is set by the API gateway (api.HeaderClientIP) after it resolved the
// caller's address. The coordinator believes it only on requests that carry the
// gateway credential (see guard).
const clientIPHeader = "X-Parsedock-Client-Ip"

type clientIPKey struct{}

// WithClientIP attaches the resolved client address to a context (audit lines).
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

func clientIP(ctx context.Context) string {
	v, _ := ctx.Value(clientIPKey{}).(string)
	return v
}

// parseClientIP accepts a single IP literal and returns its canonical form, so a
// header value can never inject separators into an audit line.
func parseClientIP(v string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(v))
	if err != nil {
		return ""
	}
	return a.Unmap().String()
}

// ValidateTokens checks the coordinator's credential pair at startup. A worker token
// only makes sense next to an internal token (without one, /v1 is unauthenticated and
// the split protects nothing), and the two must differ, otherwise a worker could still
// assert owners.
func ValidateTokens(internalToken, workerToken string) error {
	if strings.TrimSpace(workerToken) == "" {
		return nil
	}
	if strings.TrimSpace(internalToken) == "" {
		return errors.New("--worker-token requires --internal-token: the worker token only narrows what workers may call")
	}
	if workerToken == internalToken {
		return errors.New("--worker-token must differ from --internal-token")
	}
	return nil
}
