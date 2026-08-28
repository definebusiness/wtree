package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/definebusiness/wtree/internal/testutil"
)

func TestInspectCommittedIgnoreCleansPrivateExcludeFile(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	repository.CommitFile(".gitignore", "/child/\n", "ignore child")
	if err := os.Mkdir(filepath.Join(repository.Path, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.Path, "child", "probe"), []byte("probe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := createCommittedIgnoreTemp
	defer func() { createCommittedIgnoreTemp = original }()
	temporary := t.TempDir()
	var created string
	createCommittedIgnoreTemp = func(pattern string) (*committedIgnoreTemp, error) {
		file, err := os.CreateTemp(temporary, pattern)
		if err == nil {
			created = file.Name()
		}
		if err != nil {
			return nil, err
		}
		return &committedIgnoreTemp{file: file, cleanup: func() error { return os.Remove(file.Name()) }}, nil
	}
	ignored, err := NewAdapter("git").InspectCommittedIgnore(context.Background(), repository.Path, "HEAD", "child")
	if err != nil || !ignored {
		t.Fatalf("InspectCommittedIgnore() = %t, %v", ignored, err)
	}
	if created == "" {
		t.Fatal("exclude file was not created")
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exclude file remains: %v", err)
	}
}

func TestCommittedIgnoreTemporaryExcludeSourceSpelling(t *testing.T) {
	repository := testutil.NewGitRepository(t)
	if err := os.Mkdir(filepath.Join(repository.Path, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := committedIgnoreExclude([]byte("!/child/\n/child/\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	output, err := NewAdapter("git").runFact(context.Background(), repository.Path,
		"--work-tree="+repository.Path, "-c", "core.excludesFile="+path,
		"check-ignore", "-v", "--no-index", "--", "child/",
	)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _, found := bytes.Cut(bytes.TrimSpace(output), []byte{'\t'})
	if !found {
		t.Fatalf("check-ignore output = %q", output)
	}
	source, err := checkIgnoreSource(string(metadata))
	if err != nil || filepath.Clean(source) != filepath.Clean(path) {
		t.Fatalf("exclude source = %q, %v; want %q", source, err, path)
	}
}

func TestCheckIgnoreSourceDecodesGitQuotedNativePath(t *testing.T) {
	want := `C:\Users\runneradmin\AppData\Local\Temp\wtree committed ignore\exclude`
	metadata := strconv.Quote(want) + ":17:/child/"
	source, err := checkIgnoreSource(metadata)
	if err != nil || source != want {
		t.Fatalf("checkIgnoreSource(%q) = %q, %v; want %q", metadata, source, err, want)
	}
}

func TestCheckIgnoreSourceParsesCompleteGitQuotedToken(t *testing.T) {
	want := "C:\\root:17:\\café\\quoted\"\\backslash\\\\exclude"
	metadata := `"C:\\root:17:\\caf\303\251\\quoted\"\\backslash\\\\exclude":42:/child/`
	source, err := checkIgnoreSource(metadata)
	if err != nil || source != want {
		t.Fatalf("checkIgnoreSource(%q) = %q, %v; want %q", metadata, source, err, want)
	}
}

func TestCheckIgnoreSourceRejectsQuotedTokenWithoutLineDelimiter(t *testing.T) {
	for _, metadata := range []string{
		`"C:\\root:17:\\exclude"/child/`,
		`"C:\\root:17:\\exclude":x:/child/`,
		`"C:\\root:17:\\exclude":17/child/`,
	} {
		if source, err := checkIgnoreSource(metadata); err == nil {
			t.Fatalf("checkIgnoreSource(%q) = %q, nil; want delimiter error", metadata, source)
		}
	}
}

func TestCheckIgnoreSourceRejectsMalformedGitQuotedPath(t *testing.T) {
	if source, err := checkIgnoreSource(`"C:\broken:17:/child/`); err == nil {
		t.Fatalf("checkIgnoreSource() = %q, nil; want malformed quote error", source)
	}
}

func TestCommittedIgnoreExcludeJoinsSetupAndCleanupFailures(t *testing.T) {
	original := createCommittedIgnoreTemp
	defer func() { createCommittedIgnoreTemp = original }()
	path := filepath.Join(t.TempDir(), "read-only-exclude")
	if err := os.WriteFile(path, []byte("seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cleanupFailure := errors.New("injected cleanup failure")
	createCommittedIgnoreTemp = func(string) (*committedIgnoreTemp, error) {
		return &committedIgnoreTemp{file: file, cleanup: func() error { return cleanupFailure }}, nil
	}
	if _, _, err := committedIgnoreExclude([]byte("/child/\n")); err == nil || !errors.Is(err, cleanupFailure) {
		t.Fatalf("setup failure = %v; want joined cleanup failure", err)
	}
}

func TestCommittedIgnoreExcludeCleansAfterGitCommandFailure(t *testing.T) {
	original := createCommittedIgnoreTemp
	defer func() { createCommittedIgnoreTemp = original }()
	temporary := t.TempDir()
	var created string
	createCommittedIgnoreTemp = func(pattern string) (*committedIgnoreTemp, error) {
		file, err := os.CreateTemp(temporary, pattern)
		if err != nil {
			return nil, err
		}
		created = file.Name()
		return &committedIgnoreTemp{file: file, cleanup: func() error { return os.Remove(file.Name()) }}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewAdapter("git").evaluateCommittedIgnore(ctx, temporary, []committedIgnoreFile{{directory: ".", contents: []byte("/child/\n")}}, "child")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Git failure = %v, want context cancellation", err)
	}
	if created == "" {
		t.Fatal("exclude file was not created before Git command failure")
	}
	if _, err := os.Lstat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exclude remains after Git command failure: %v", err)
	}
}
