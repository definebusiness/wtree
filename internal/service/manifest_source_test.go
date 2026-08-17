package service

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManifestSourceLoadsEquivalentLocalAndHTTPAndNormalizes(t *testing.T) {
	contents := []byte("version: 1\n")
	directory := t.TempDir()
	local := filepath.Join(directory, "manifest.yml")
	if err := os.WriteFile(local, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { _, _ = response.Write(contents) }))
	defer server.Close()

	localLoaded, err := NewManifestSourceLoader().Load(context.Background(), filepath.Join(directory, ".", "manifest.yml"))
	if err != nil {
		t.Fatal(err)
	}
	httpLoaded, err := NewManifestSourceLoader().Load(context.Background(), strings.Replace(server.URL, "http://", "HTTP://", 1))
	if err != nil {
		t.Fatal(err)
	}
	if localLoaded.Kind != ManifestSourceLocal || localLoaded.Source != local || string(localLoaded.Bytes()) != string(contents) {
		t.Fatalf("local source = %#v, %q", localLoaded, localLoaded.Bytes())
	}
	if httpLoaded.Kind != ManifestSourceHTTP || !strings.HasPrefix(httpLoaded.Source, "http://") || string(httpLoaded.Bytes()) != string(contents) {
		t.Fatalf("HTTP source = %#v, %q", httpLoaded, httpLoaded.Bytes())
	}
	copy := localLoaded.Bytes()
	copy[0] = 'X'
	if string(localLoaded.Bytes()) != string(contents) {
		t.Fatal("LoadedManifestSource exposed mutable bytes")
	}
}

func TestManifestSourceRejectsLocalKindsAndOversize(t *testing.T) {
	directory := t.TempDir()
	regular := filepath.Join(directory, "regular")
	if err := os.WriteFile(regular, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "link")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	oversize := filepath.Join(directory, "oversize")
	if err := os.WriteFile(oversize, make([]byte, MaxPortableManifestBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{directory, symlink, oversize} {
		if _, err := NewManifestSourceLoader().Load(context.Background(), source); err == nil {
			t.Errorf("Load(%q) error = nil", source)
		}
	}
}

func TestManifestSourceAcceptsExactLimitAndHonorsCancellationAndTLS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exact-limit.yml")
	if err := os.WriteFile(path, make([]byte, MaxPortableManifestBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := NewManifestSourceLoader().Load(context.Background(), path); err != nil || len(loaded.Bytes()) != int(MaxPortableManifestBytes) {
		t.Fatalf("exact-limit local source = %d bytes, %v", len(loaded.Bytes()), err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewManifestSourceLoader().Load(ctx, "http://127.0.0.1:1/manifest"); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("cancellation error = %v", err)
	}

	secure := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { _, _ = response.Write([]byte("secure")) }))
	defer secure.Close()
	if _, err := NewManifestSourceLoader().Load(context.Background(), secure.URL); err == nil || strings.Contains(err.Error(), "certificate") {
		// The failure is intentionally classified and must not expose TLS internals.
		if err == nil {
			t.Fatal("default TLS verification trusted an unknown test certificate")
		}
		t.Fatalf("TLS error leaked transport detail: %v", err)
	}
}

func TestManifestSourceHTTPPolicyIsBoundedAndRedacted(t *testing.T) {
	secret := "manifest-secret-canary"
	status := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = response.Write([]byte(secret))
	}))
	defer status.Close()
	if _, err := NewManifestSourceLoader().Load(context.Background(), status.URL); err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("status error = %v", err)
	}

	chunked := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.(http.Flusher).Flush()
		_, _ = response.Write(make([]byte, MaxPortableManifestBytes+1))
	}))
	defer chunked.Close()
	if _, err := NewManifestSourceLoader().Load(context.Background(), chunked.URL); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("chunked oversize error = %v", err)
	}

	timeout := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = response.Write([]byte("late"))
	}))
	defer timeout.Close()
	client := timeout.Client()
	client.Timeout = 5 * time.Millisecond
	if _, err := NewManifestSourceLoaderWithClient(client).Load(context.Background(), timeout.URL); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}

	credentialSource := "https://user:" + secret + "@example.invalid/project.wtree.yml"
	if _, err := NewManifestSourceLoader().Load(context.Background(), credentialSource); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("userinfo error = %v", err)
	}
	if _, err := NewManifestSourceLoader().Load(context.Background(), "ftp://example.invalid/project.wtree.yml"); err == nil {
		t.Fatal("unsupported manifest source scheme accepted")
	}
}

func TestManifestSourceRedirectLimitLoopAndDowngrade(t *testing.T) {
	var loop *httptest.Server
	loop = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, loop.URL+request.URL.Path+"x", http.StatusFound)
	}))
	defer loop.Close()
	if _, err := NewManifestSourceLoader().Load(context.Background(), loop.URL); err == nil || !strings.Contains(err.Error(), "redirect limit") {
		t.Fatalf("redirect loop error = %v", err)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { _, _ = response.Write([]byte("no")) }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, plain.URL, http.StatusFound)
	}))
	defer secure.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // hermetic test server only
	client := &http.Client{Transport: transport}
	if _, err := NewManifestSourceLoaderWithClient(client).Load(context.Background(), secure.URL); err == nil || !strings.Contains(strings.ToLower(err.Error()), "https to http") {
		t.Fatalf("downgrade error = %v", err)
	}
}
