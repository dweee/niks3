package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mic92/niks3/server/signing"
)

type upstreamFilter struct {
	stores []*url.URL
	keys   map[string]ed25519.PublicKey
	http   *http.Client
}

// ConfigureUpstreams enables filtering for the specified public HTTPS caches.
// The destination and its hostname aliases must be excluded by the caller.
func (c *Client) ConfigureUpstreams(stores, keys []string) error {
	if len(stores) == 0 {
		return nil
	}

	filter := &upstreamFilter{
		keys: make(map[string]ed25519.PublicKey),
		http: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}
	for _, value := range stores {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("upstream substituters must be public HTTPS cache URLs")
		}
		if strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), strings.TrimSuffix(c.baseURL.Hostname(), ".")) {
			return fmt.Errorf("the destination cache cannot also be an upstream substituter")
		}
		filter.stores = append(filter.stores, u)
	}
	for _, value := range keys {
		name, encoded, ok := strings.Cut(value, ":")
		key, err := base64.StdEncoding.DecodeString(encoded)
		if !ok || name == "" || err != nil || len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid upstream signing public key")
		}
		filter.keys[name] = key
	}
	if len(filter.keys) == 0 {
		return fmt.Errorf("upstream substituters require trusted public keys")
	}
	c.upstream = filter
	return nil
}

// ConfigureUpstreamsFromEnvironment applies the same policy to CLI and hook pushes.
func (c *Client) ConfigureUpstreamsFromEnvironment() error {
	return c.ConfigureUpstreams(
		strings.Fields(os.Getenv("NIKS3_UPSTREAM_SUBSTITUTERS")),
		strings.Fields(os.Getenv("NIKS3_UPSTREAM_PUBLIC_KEYS")),
	)
}

func (f *upstreamFilter) available(ctx context.Context, paths map[string]*PathInfo, keep map[string]bool) map[string]bool {
	jobs := make(chan string)
	external := make(map[string]bool)
	var mu sync.Mutex
	var workers sync.WaitGroup
	for range min(32, len(paths)) {
		workers.Go(func() {
			for path := range jobs {
				if f.contains(ctx, path, paths[path]) {
					mu.Lock()
					external[path] = true
					mu.Unlock()
				}
			}
		})
	}
queue:
	for path, info := range paths {
		// Realisation metadata for CA paths isn't part of a narinfo.
		if keep[path] || info.CA != nil {
			continue
		}
		select {
		case jobs <- path:
		case <-ctx.Done():
			break queue
		}
	}
	close(jobs)
	workers.Wait()
	slog.Info("Checked upstream substituters", "available", len(external), "closure_paths", len(paths))
	return external
}

func (f *upstreamFilter) contains(ctx context.Context, path string, info *PathInfo) bool {
	hash, err := GetStorePathHash(path)
	if err != nil {
		return false
	}
	narHash, err := ConvertHashToNix32(info.NarHash.String())
	if err != nil {
		return false
	}
	fingerprint, err := signing.GenerateFingerprint(&signing.NarInfo{
		StorePath: path, NarHash: narHash, NarSize: info.NarSize, References: info.References,
	})
	if err != nil {
		return false
	}
	for _, store := range f.stores {
		if ctx.Err() != nil {
			return false
		}
		u := store.JoinPath(hash + ".narinfo")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			continue
		}
		resp, err := f.http.Do(req)
		if err != nil {
			continue
		}
		var narURL string
		var fileSize int64
		if resp.StatusCode == http.StatusOK {
			narURL, fileSize = f.matchNarinfo(resp.Body, fingerprint, path)
		}
		_ = resp.Body.Close()
		if narURL == "" {
			continue
		}
		object, err := u.Parse(narURL)
		if err != nil || object.Scheme != "https" || object.User != nil || !strings.EqualFold(object.Host, u.Host) {
			continue
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodHead, object.String(), nil)
		if err != nil {
			continue
		}
		resp, err = f.http.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && resp.ContentLength == fileSize {
				return true
			}
		}
	}
	return false
}

func (f *upstreamFilter) matchNarinfo(body io.Reader, localFingerprint []byte, path string) (string, int64) {
	scan := bufio.NewScanner(io.LimitReader(body, 1<<20))
	scan.Buffer(make([]byte, 4096), 1<<20)
	fields := make(map[string]string)
	var signatures []string
	for scan.Scan() {
		name, value, ok := strings.Cut(scan.Text(), ": ")
		if !ok {
			return "", 0
		}
		switch name {
		case "Sig":
			signatures = append(signatures, value)
		case "StorePath", "URL", "NarHash", "NarSize", "References", "FileSize":
			if _, seen := fields[name]; seen {
				return "", 0
			}
			fields[name] = value
		}
	}
	narSize, err := strconv.ParseUint(fields["NarSize"], 10, 64)
	if scan.Err() != nil || err != nil || fields["StorePath"] != path {
		return "", 0
	}
	fileSize, err := strconv.ParseInt(fields["FileSize"], 10, 64)
	if err != nil || fileSize <= 0 {
		return "", 0
	}
	refs := strings.Fields(fields["References"])
	for i := range refs {
		refs[i] = "/nix/store/" + refs[i]
	}
	fingerprint, err := signing.GenerateFingerprint(&signing.NarInfo{
		StorePath: path, NarHash: fields["NarHash"], NarSize: narSize, References: refs,
	})
	if err != nil || !bytes.Equal(fingerprint, localFingerprint) {
		return "", 0
	}
	for _, value := range signatures {
		name, encoded, _ := strings.Cut(value, ":")
		if key, ok := f.keys[name]; ok {
			sig, err := base64.StdEncoding.DecodeString(encoded)
			if err == nil && ed25519.Verify(key, localFingerprint, sig) {
				return fields["URL"], fileSize
			}
		}
	}
	return "", 0
}

// firstLocalPaths returns the first locally retained paths on each branch.
func firstLocalPaths(refs []string, paths map[string]*PathInfo, external map[string]bool) []string {
	seen := make(map[string]bool)
	var result []string
	var visit func(string)
	visit = func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		if !external[path] {
			result = append(result, path)
			return
		}
		for _, ref := range paths[path].References {
			visit(ref)
		}
	}
	for _, ref := range refs {
		visit(ref)
	}
	return result
}

// localClosure bridges external references to the first local paths for
// server-side retention. Published narinfos still use the original references.
func localClosure(roots []string, paths map[string]*PathInfo, external map[string]bool) ([]string, map[string]*PathInfo) {
	graph := make(map[string]*PathInfo, len(paths)-len(external))
	for path, info := range paths {
		if !external[path] {
			copy := *info
			copy.References = firstLocalPaths(info.References, paths, external)
			graph[path] = &copy
		}
	}
	return firstLocalPaths(roots, paths, external), graph
}
