package netx

import "context"

// Only the authenticated process gateway may trust a child-managed CookieJar.
type processCookieKey struct{}
type explicitCookieKey struct{}

// WithExplicitCookies suppresses configured-cookie injection, leaving the
// adapter's scoped request/session cookies intact. Redirect stripping still wins.
func WithExplicitCookies(ctx context.Context) context.Context {
	return context.WithValue(ctx, explicitCookieKey{}, true)
}

// WithoutCookies is for genuinely anonymous requests, including short-link hops.
func WithoutCookies(ctx context.Context) context.Context {
	return context.WithValue(ctx, redirectCredentialKey{}, true)
}
