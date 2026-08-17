package git

import (
	"context"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/testutil"
)

func TestPublishedRepositoryFactsRejectsGenerationChange(t *testing.T) {
	repository := testutil.NewPushedGitRepository(t)
	repository.CommitFile("published", "one\n", "published")
	publishedFactsBeforeRevalidation = func() {
		repository.GitRepository.CommitFile("changed", "two\n", "changed locally")
	}
	t.Cleanup(func() { publishedFactsBeforeRevalidation = nil })
	_, err := NewAdapter("git").PublishedRepositoryFacts(context.Background(), repository.Path)
	if err == nil || !strings.Contains(err.Error(), "advertised upstream") {
		t.Fatalf("PublishedRepositoryFacts() error = %v", err)
	}
}
