package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"go.opentelemetry.io/otel/trace"
)

// Role is resolved from tenant membership stored by IVP. Roles in bearer token
// claims are deliberately ignored.
type Role string

const (
	RoleReader     Role = "reader"
	RoleExecutor   Role = "executor"
	RoleMaintainer Role = "maintainer"
	RoleAuditor    Role = "auditor"
	RoleAdmin      Role = "admin"
)

type Principal struct {
	Subject  string
	TenantID string
	UserID   string
	Role     Role
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}

type Membership struct {
	UserID   string
	TenantID string
	Role     Role
	Disabled bool
}

// MembershipResolver must consult server-controlled membership data.
type MembershipResolver interface {
	ResolveMembership(ctx context.Context, subject, tenantID string) (Membership, error)
}

type Authenticator struct {
	Verifier       *oidc.IDTokenVerifier
	Members        MembershipResolver
	Clock          func() time.Time
	RequiredClaims map[string]string
}

func NewOIDCAuthenticator(ctx context.Context, issuer, audience string, members MembershipResolver) (*Authenticator, error) {
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(audience) == "" || members == nil {
		return nil, errors.New("OIDC issuer, audience, and membership resolver are required")
	}
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &Authenticator{Verifier: p.Verifier(&oidc.Config{ClientID: audience}), Members: members, Clock: time.Now}, nil
}

func NewOIDCAuthenticatorWithClaims(ctx context.Context, issuer, audience string, members MembershipResolver, required map[string]string) (*Authenticator, error) {
	a, err := NewOIDCAuthenticator(ctx, issuer, audience, members)
	if err != nil {
		return nil, err
	}
	a.RequiredClaims = make(map[string]string, len(required))
	for k, v := range required {
		a.RequiredClaims[k] = v
	}
	return a, nil
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a == nil || a.Verifier == nil || a.Members == nil {
			authError(w, r, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "authentication is not configured")
			return
		}
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			authError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if token == "" || strings.ContainsAny(token, "\r\n\t ") {
			authError(w, r, http.StatusUnauthorized, "INVALID_TOKEN", "invalid bearer token")
			return
		}
		idToken, err := a.Verifier.Verify(r.Context(), token)
		if err != nil {
			authError(w, r, http.StatusUnauthorized, "INVALID_TOKEN", "invalid bearer token")
			return
		}
		var claims map[string]any
		if err := idToken.Claims(&claims); err != nil || idToken.Subject == "" || claims["sub"] != idToken.Subject {
			authError(w, r, http.StatusUnauthorized, "INVALID_CLAIMS", "required identity claims are missing")
			return
		}
		for name, want := range a.RequiredClaims {
			actual, ok := claims[name]
			if !ok || fmt.Sprint(actual) != want {
				authError(w, r, http.StatusUnauthorized, "INVALID_CLAIMS", "required identity claims are missing")
				return
			}
		}
		tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
		if tenantID == "" {
			authError(w, r, http.StatusBadRequest, "TENANT_REQUIRED", "tenant selection required")
			return
		}
		membership, err := a.Members.ResolveMembership(r.Context(), idToken.Subject, tenantID)
		if err != nil || membership.Disabled || membership.UserID == "" || membership.TenantID != tenantID || !knownRole(membership.Role) {
			authError(w, r, http.StatusForbidden, "TENANT_MEMBERSHIP_DENIED", "tenant membership denied")
			return
		}
		p := Principal{Subject: idToken.Subject, TenantID: membership.TenantID, UserID: membership.UserID, Role: membership.Role}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func knownRole(role Role) bool {
	switch role {
	case RoleReader, RoleExecutor, RoleMaintainer, RoleAuditor, RoleAdmin:
		return true
	default:
		return false
	}
}

func HasRole(actual Role, allowed ...Role) bool {
	for _, role := range allowed {
		if actual == role || actual == RoleAdmin || (role == RoleReader && (actual == RoleExecutor || actual == RoleMaintainer || actual == RoleAuditor)) || (role == RoleExecutor && actual == RoleMaintainer) {
			return true
		}
	}
	return false
}

func authError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	traceID := ""
	if sc := trace.SpanFromContext(r.Context()).SpanContext(); sc.IsValid() {
		traceID = sc.TraceID().String()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	response := map[string]any{"error": map[string]any{"code": code, "message": message, "retryable": status == http.StatusServiceUnavailable}, "request_id": r.Header.Get("X-Request-ID")}
	if traceID != "" {
		response["trace_id"] = traceID
	}
	_ = json.NewEncoder(w).Encode(response)
}
