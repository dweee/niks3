package client

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/Mic92/niks3/server/signing"
)

func TestUpstreamRequiresExactSignedAvailableObject(t *testing.T) {
	const path = "/nix/store/00000000000000000000000000000000-hello"
	const narHash = "sha256:0000000000000000000000000000000000000000000000000000"
	const ref = "/nix/store/11111111111111111111111111111111-lib"
	info := &PathInfo{NarHash: Hash{hash: narHash}, NarSize: 1024, References: []string{ref}}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := signing.GenerateFingerprint(&signing.NarInfo{
		StorePath: path, NarHash: narHash, NarSize: 1024, References: []string{ref},
	})
	if err != nil {
		t.Fatal(err)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(private, fingerprint))
	narinfo := fmt.Sprintf("StorePath: %s\nURL: nar/hello.nar.zst\nFileSize: 512\nNarHash: %s\nNarSize: 1024\nReferences: %s\nSig: upstream:%s\n", path, narHash, strings.TrimPrefix(ref, "/nix/store/"), sig)

	for _, tc := range []struct {
		name, metadata string
		narStatus      int
		want           bool
	}{
		{"exact", narinfo, http.StatusOK, true},
		{"exact without FileSize", strings.Replace(narinfo, "FileSize: 512\n", "", 1), http.StatusOK, true},
		{"wrong file size", strings.Replace(narinfo, "FileSize: 512", "FileSize: 513", 1), http.StatusOK, false},
		{"different content", strings.Replace(narinfo, "NarSize: 1024", "NarSize: 1025", 1), http.StatusOK, false},
		{"different refs", strings.Replace(narinfo, "-lib", "-other", 1), http.StatusOK, false},
		{"untrusted signature", strings.Replace(narinfo, "Sig: upstream:", "Sig: other:", 1), http.StatusOK, false},
		{"bad signature", strings.Replace(narinfo, sig, "invalid", 1), http.StatusOK, false},
		{"undownloadable", narinfo, http.StatusNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, ".narinfo"):
					_, _ = fmt.Fprint(w, tc.metadata)
				case r.Method == http.MethodHead && r.URL.Path == "/nar/hello.nar.zst":
					w.Header().Set("Content-Length", "512")
					w.WriteHeader(tc.narStatus)
				default:
					http.NotFound(w, r)
				}
			}))
			defer s.Close()
			store, err := url.Parse(s.URL)
			if err != nil {
				t.Fatal(err)
			}
			filter := &upstreamFilter{
				stores: []*url.URL{store},
				keys:   map[string]ed25519.PublicKey{"upstream": public},
				http:   s.Client(),
			}
			if got := filter.contains(context.Background(), path, info); got != tc.want {
				t.Fatalf("available = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLocalClosureBridgesExternalDependencies(t *testing.T) {
	paths := map[string]*PathInfo{
		"local-root":    {References: []string{"upstream", "local-leaf"}},
		"upstream":      {References: []string{"upstream-peer", "local-leaf"}},
		"upstream-peer": {References: []string{"upstream", "other-leaf"}},
		"local-leaf":    {},
		"other-leaf":    {},
	}
	external := map[string]bool{"upstream": true, "upstream-peer": true}
	roots, graph := localClosure([]string{"local-root", "upstream"}, paths, external)
	if !slices.Equal(roots, []string{"local-root", "other-leaf", "local-leaf"}) {
		t.Fatalf("upload roots: %v", roots)
	}
	if len(graph) != 3 || !slices.Equal(graph["local-root"].References, []string{"other-leaf", "local-leaf"}) {
		t.Fatalf("retained graph has %d nodes, root refs %v", len(graph), graph["local-root"].References)
	}
	if !slices.Equal(paths["local-root"].References, []string{"upstream", "local-leaf"}) {
		t.Fatal("original narinfo references were modified")
	}
}
