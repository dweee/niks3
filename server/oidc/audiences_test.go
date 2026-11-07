package oidc_test

import (
	"testing"

	"github.com/Mic92/niks3/server/oidc"
	"github.com/Mic92/niks3/server/oidc/oidctest"
	"github.com/golang-jwt/jwt/v5"
)

func TestDistinctAudiencesKeepScopesSeparate(t *testing.T) {
	t.Parallel()
	m := oidctest.StartMockOIDC(t)
	provider := func(audience string, scope oidc.Scope) *oidc.ProviderConfig {
		return &oidc.ProviderConfig{
			Issuer: m.Issuer(), Audience: audience,
			Rules: []oidc.Rule{{
				BoundClaims: map[string][]string{"repository": {"example/tool"}, "event_name": {"push"}},
				Scopes:      []oidc.Scope{scope},
			}},
		}
	}
	ctx, validator := oidctest.NewValidator(t, oidc.Config{
		AllowInsecure: true,
		Providers: map[string]*oidc.ProviderConfig{
			"reader": provider("cache-read", oidc.ScopeRead),
			"writer": provider("cache-write", oidc.ScopeWrite),
		},
	})
	for _, tc := range []struct {
		audience, repository, event string
		want                        oidc.Scope
	}{
		{"cache-read", "example/tool", "push", oidc.ScopeRead},
		{"cache-write", "example/tool", "push", oidc.ScopeWrite},
		{"unrelated", "example/tool", "push", ""},
		{"cache-read", "example/other", "push", ""},
		{"cache-write", "example/tool", "pull_request", ""},
		{"cache-read", "example/tool", "pull_request", ""},
	} {
		t.Run(tc.audience+"/"+tc.repository+"/"+tc.event, func(t *testing.T) {
			claims, err := validator.ValidateToken(ctx, oidctest.SignToken(t, m, jwt.MapClaims{
				"aud": tc.audience, "repository": tc.repository, "event_name": tc.event,
			}))
			if tc.want == "" {
				if err == nil {
					t.Fatal("unexpected token grant")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(claims.Scopes) != 1 || claims.Scopes[0] != tc.want {
				t.Fatalf("scopes = %v; want only %s", claims.Scopes, tc.want)
			}
			if claims.Issuer != m.Issuer() {
				t.Fatal("lost original issuer")
			}
		})
	}
	if _, err := validator.ValidateToken(ctx, oidctest.SignToken(t, m, jwt.MapClaims{
		"aud": []string{"cache-read", "cache-write"}, "repository": "example/tool", "event_name": "push",
	})); err == nil {
		t.Fatal("ambiguous multi-audience token was accepted")
	}
}

func TestIdenticalIssuerAndAudienceRemainRejected(t *testing.T) {
	t.Parallel()
	config := oidc.Config{Providers: map[string]*oidc.ProviderConfig{
		"first":  {Issuer: "https://issuer.example", Audience: "same", BoundSubject: []string{"ci:*"}},
		"second": {Issuer: "https://issuer.example", Audience: "same", BoundSubject: []string{"ci:*"}},
	}}
	if _, err := oidc.LoadConfig(oidctest.WriteConfig(t, config)); err == nil {
		t.Fatal("ambiguous duplicate provider accepted")
	}
}
