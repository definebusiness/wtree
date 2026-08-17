package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/definebusiness/wtree/internal/config"
)

const MaxPortableManifestBytes int64 = 1 << 20

type ManifestSourceKind string

const (
	ManifestSourceLocal ManifestSourceKind = "local"
	ManifestSourceHTTP  ManifestSourceKind = "http"
)

// LoadedManifestSource is a bounded, credential-free source observation.
// Bytes returns a copy so a caller cannot mutate the loader's observation.
type LoadedManifestSource struct {
	Kind   ManifestSourceKind `json:"kind"`
	Source string             `json:"source"`
	data   []byte
}

func (source LoadedManifestSource) Bytes() []byte { return append([]byte(nil), source.data...) }

type ManifestSourceFileSystem interface {
	Abs(string) (string, error)
	Lstat(string) (os.FileInfo, error)
	Open(string) (ManifestSourceFile, error)
}

type ManifestSourceFile interface {
	io.ReadCloser
	Stat() (os.FileInfo, error)
}

type osManifestSourceFileSystem struct{}

func (osManifestSourceFileSystem) Abs(value string) (string, error)        { return filepath.Abs(value) }
func (osManifestSourceFileSystem) Lstat(value string) (os.FileInfo, error) { return os.Lstat(value) }
func (osManifestSourceFileSystem) Open(value string) (ManifestSourceFile, error) {
	return os.Open(value)
}

// ManifestSourceLoader owns the only local/HTTP(S) manifest-fetch policy.
type ManifestSourceLoader struct {
	client *http.Client
	fs     ManifestSourceFileSystem
}

func NewManifestSourceLoader() *ManifestSourceLoader {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Manifest loading must not inherit proxy credentials or routing from the
	// ambient process. TLS verification and all other secure defaults remain.
	transport.Proxy = nil
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	client.CheckRedirect = manifestRedirectPolicy
	return &ManifestSourceLoader{client: client, fs: osManifestSourceFileSystem{}}
}

// NewManifestSourceLoaderWithClient is a narrow hermetic-test seam. A nil
// client still selects the production-safe client rather than a partial setup.
func NewManifestSourceLoaderWithClient(client *http.Client) *ManifestSourceLoader {
	return NewManifestSourceLoaderWith(client, nil)
}

// NewManifestSourceLoaderWith is the complete loader test seam. Nil values
// retain production-safe defaults instead of constructing a partial loader.
func NewManifestSourceLoaderWith(client *http.Client, filesystem ManifestSourceFileSystem) *ManifestSourceLoader {
	loader := NewManifestSourceLoader()
	if client != nil {
		copy := *client
		copy.Jar = nil
		copy.CheckRedirect = manifestRedirectPolicy
		if copy.Timeout == 0 || copy.Timeout > 30*time.Second {
			copy.Timeout = 30 * time.Second
		}
		loader.client = &copy
	}
	if filesystem != nil {
		loader.fs = filesystem
	}
	return loader
}

func manifestRedirectPolicy(request *http.Request, via []*http.Request) error {
	if len(via) > 5 {
		return errors.New("manifest redirect limit exceeded")
	}
	if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && strings.EqualFold(request.URL.Scheme, "http") {
		return errors.New("manifest redirect from HTTPS to HTTP is not allowed")
	}
	if request.URL.User != nil {
		return errors.New("manifest redirect contains user information")
	}
	return nil
}

func (loader *ManifestSourceLoader) Load(ctx context.Context, value string) (LoadedManifestSource, error) {
	if loader == nil {
		loader = NewManifestSourceLoader()
	}
	kind, normalized, err := loader.normalize(value)
	if err != nil {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("manifest source %q: %w", redactManifestSource(value), err))
	}
	return loader.loadNormalized(ctx, kind, normalized)
}

func (loader *ManifestSourceLoader) normalize(value string) (ManifestSourceKind, string, error) {
	if loader == nil {
		loader = NewManifestSourceLoader()
	}
	return normalizeManifestSource(value, loader.fs)
}

func (loader *ManifestSourceLoader) loadNormalized(ctx context.Context, kind ManifestSourceKind, normalized string) (LoadedManifestSource, error) {
	if kind == ManifestSourceLocal {
		return loader.loadLocal(normalized)
	}
	return loader.loadHTTP(ctx, normalized)
}

func normalizeManifestSource(value string, filesystem ManifestSourceFileSystem) (ManifestSourceKind, string, error) {
	if value == "" {
		return "", "", errors.New("is required")
	}
	parsed, parseErr := url.Parse(value)
	isHTTP := strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://")
	if isHTTP {
		if parseErr != nil || parsed == nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
			return "", "", errors.New("is not a credential-free absolute HTTP(S) URL")
		}
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		normalized := parsed.String()
		if err := config.ValidateManifestSource(normalized); err != nil {
			return "", "", err
		}
		return ManifestSourceHTTP, normalized, nil
	}
	// On Windows an absolute drive path parses as a one-letter URL scheme.
	// Classify native absolute paths before rejecting other URI schemes.
	if parseErr == nil && parsed.Scheme != "" && !filepath.IsAbs(value) {
		return "", "", errors.New("must be a local path or an absolute HTTP(S) URL")
	}
	abs, err := filesystem.Abs(value)
	if err != nil {
		return "", "", err
	}
	abs = filepath.Clean(abs)
	if err := config.ValidateManifestSource(abs); err != nil {
		return "", "", err
	}
	return ManifestSourceLocal, abs, nil
}

func (loader *ManifestSourceLoader) loadLocal(path string) (LoadedManifestSource, error) {
	info, err := loader.fs.Lstat(path)
	if err != nil {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("read local manifest %q: %w", path, err))
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("local manifest %q must be a regular non-symlink file", path))
	}
	if info.Size() > MaxPortableManifestBytes {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("local manifest %q exceeds the 1 MiB limit", path))
	}
	file, err := loader.fs.Open(path)
	if err != nil {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("open local manifest %q: %w", path, err))
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("local manifest %q changed while it was opened", path))
	}
	if openedInfo.Size() > MaxPortableManifestBytes {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("local manifest %q exceeds the 1 MiB limit", path))
	}
	data, err := readBoundedManifest(file)
	if err != nil {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("read local manifest %q: %w", path, err))
	}
	afterInfo, err := file.Stat()
	if err != nil || afterInfo.Size() != openedInfo.Size() || afterInfo.Mode() != openedInfo.Mode() || !afterInfo.ModTime().Equal(openedInfo.ModTime()) {
		return LoadedManifestSource{}, NewError(ErrorConflict, fmt.Errorf("local manifest %q changed while it was read", path))
	}
	return LoadedManifestSource{Kind: ManifestSourceLocal, Source: path, data: data}, nil
}

func (loader *ManifestSourceLoader) loadHTTP(ctx context.Context, source string) (LoadedManifestSource, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("fetch manifest %q: invalid request", redactManifestSource(source)))
	}
	response, err := loader.client.Do(request)
	if err != nil {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("fetch manifest %q: %s", redactManifestSource(source), classifyManifestHTTPError(err)))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("fetch manifest %q: HTTP status %d", redactManifestSource(source), response.StatusCode))
	}
	data, err := readBoundedManifest(response.Body)
	if err != nil {
		return LoadedManifestSource{}, NewError(ErrorValidation, fmt.Errorf("fetch manifest %q: %w", redactManifestSource(source), err))
	}
	return LoadedManifestSource{Kind: ManifestSourceHTTP, Source: source, data: data}, nil
}

func readBoundedManifest(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxPortableManifestBytes+1))
	if err != nil {
		return nil, errors.New("response read failed")
	}
	if int64(len(data)) > MaxPortableManifestBytes {
		return nil, errors.New("manifest exceeds the 1 MiB limit")
	}
	return data, nil
}

func classifyManifestHTTPError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return "request timed out"
	}
	message := strings.ToLower(err.Error())
	for _, safe := range []string{"redirect limit exceeded", "https to http", "user information"} {
		if strings.Contains(message, safe) {
			return safe
		}
	}
	return "request failed"
}

func redactManifestSource(value string) string {
	parsed, err := url.Parse(value)
	if err == nil && parsed.User != nil {
		parsed.User = url.User("REDACTED")
		value = parsed.String()
	}
	return redactCredentialShapes(value)
}

func redactCredentialShapes(value string) string {
	// Keep diagnostics useful while removing common URL credential shapes.
	if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
		parsed.User = url.User("REDACTED")
		return parsed.String()
	}
	return credentialURLPattern.ReplaceAllString(value, `${1}REDACTED@`)
}

func boundedRedactedDiagnostic(value string) string {
	return boundedRedactedDiagnosticLimit(value, 8192)
}

func boundedRedactedDiagnosticLimit(value string, limit int) string {
	value = redactCredentialShapes(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

var credentialURLPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@`)
