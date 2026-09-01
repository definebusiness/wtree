package cli

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/definebusiness/wtree/internal/service"
)

func TestRenderUpstreamStatus(t *testing.T) {
	tests := []struct {
		name       string
		repository service.RepositoryStatus
		want       string
	}{
		{name: "no upstream", repository: service.RepositoryStatus{Status: "clean"}, want: "none"},
		{name: "up to date", repository: service.RepositoryStatus{Status: "clean", Upstream: true}, want: "up-to-date"},
		{name: "ahead", repository: service.RepositoryStatus{Status: "clean", Upstream: true, Ahead: 12}, want: "ahead 12"},
		{name: "behind", repository: service.RepositoryStatus{Status: "clean", Upstream: true, Behind: 3}, want: "behind 3"},
		{name: "diverged", repository: service.RepositoryStatus{Status: "clean", Upstream: true, Ahead: 5, Behind: 8}, want: "diverged (ahead 5, behind 8)"},
		{name: "detached", repository: service.RepositoryStatus{Status: "detached", Detached: true, Upstream: true, Ahead: 1}, want: "n/a"},
		{name: "missing", repository: service.RepositoryStatus{Status: "missing", Missing: true}, want: "n/a"},
		{name: "stale state", repository: service.RepositoryStatus{Status: "stale-state", StaleState: true}, want: "n/a"},
		{name: "unknown repository", repository: service.RepositoryStatus{Status: "unknown-repository", UnknownRepository: true}, want: "n/a"},
		{name: "modified and ahead", repository: service.RepositoryStatus{Status: "modified", Modified: true, Upstream: true, Ahead: 1}, want: "ahead 1"},
		{name: "branch mismatch and behind", repository: service.RepositoryStatus{Status: "branch-mismatch", BranchMismatch: true, Upstream: true, Behind: 2}, want: "behind 2"},
		{name: "mount mismatch and up to date", repository: service.RepositoryStatus{Status: "mount-mismatch", MountMismatch: true, Upstream: true}, want: "up-to-date"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := renderUpstreamStatus(test.repository); got != test.want {
				t.Fatalf("renderUpstreamStatus(%#v) = %q, want %q", test.repository, got, test.want)
			}
		})
	}
}

func TestRenderWorkspaceStatusIncludesDeterministicUpstreamColumn(t *testing.T) {
	value := service.WorkspaceStatus{
		Workspace: "feature/status",
		Repositories: []service.RepositoryStatus{
			{ID: "root", Branch: "feature/status", Mount: ".", Status: "clean", Upstream: true, Behind: 1},
			{ID: "backend", Branch: "feature/status", Mount: "api space", Status: "modified", Modified: true, Upstream: true, Ahead: 2, Behind: 3},
		},
	}
	var output bytes.Buffer
	if err := renderWorkspaceStatus(&output, value); err != nil {
		t.Fatal(err)
	}
	want := "Workspace: feature/status\n\nREPOSITORY  BRANCH          MOUNT      STATUS    UPSTREAM\nroot        feature/status  .          clean     behind 1\nbackend     feature/status  api space  modified  diverged (ahead 2, behind 3)\n"
	if got := output.String(); got != want {
		t.Fatalf("human status = %q, want %q", got, want)
	}
}

func TestRenderWorkspaceStatusAppendsLocalDriftOnlyWhenPresent(t *testing.T) {
	value := service.WorkspaceStatus{Workspace: "default", Repositories: []service.RepositoryStatus{{ID: "root", Mount: ".", Status: "clean"}}, Drift: []service.StatusDrift{{ID: "root", Origin: "manifest", Check: "checkout", Status: "declared-absent"}}}
	var output bytes.Buffer
	if err := renderWorkspaceStatus(&output, value); err != nil {
		t.Fatal(err)
	}
	want := "Workspace: default\n\nREPOSITORY  BRANCH  MOUNT  STATUS  UPSTREAM\nroot                .      clean   none\n\nLocal drift:\nREPOSITORY  ORIGIN    CHECK     STATUS\nroot        manifest  checkout  declared-absent\n"
	if output.String() != want {
		t.Fatalf("human status = %q, want %q", output.String(), want)
	}
}

func TestRenderWorkspaceStatusAppendsHookSetupOnlyWhenPresent(t *testing.T) {
	value := service.WorkspaceStatus{Workspace: "default", Repositories: []service.RepositoryStatus{{ID: "root", Mount: ".", Status: "clean"}}, Setup: []service.HookSetupStatus{{Event: "post-create", State: "failed", NextHookID: "setup", CompletedCount: 1, FailureKind: "non-zero-exit"}}}
	var output bytes.Buffer
	if err := renderWorkspaceStatus(&output, value); err != nil {
		t.Fatal(err)
	}
	want := "Workspace: default\n\nREPOSITORY  BRANCH  MOUNT  STATUS  UPSTREAM\nroot                .      clean   none\n\nSetup:\nEVENT        STATE   NEXT   COMPLETED  FAILURE\npost-create  failed  setup  1          non-zero-exit\n"
	if output.String() != want {
		t.Fatalf("hook setup status = %q, want %q", output.String(), want)
	}
}

func TestRenderWorkspaceStatusKeepsUnknownIdentityUpstreamNA(t *testing.T) {
	value := service.WorkspaceStatus{Workspace: "default", Repositories: []service.RepositoryStatus{{ID: "root", Mount: ".", UnknownRepository: true, IdentityMismatch: true, Status: "unknown-repository"}}, Drift: []service.StatusDrift{{ID: "root", Origin: "checkout", Check: "identity", Status: "mismatch"}}}
	var output bytes.Buffer
	if err := renderWorkspaceStatus(&output, value); err != nil {
		t.Fatal(err)
	}
	want := "Workspace: default\n\nREPOSITORY  BRANCH  MOUNT  STATUS              UPSTREAM\nroot                .      unknown-repository  n/a\n\nLocal drift:\nREPOSITORY  ORIGIN    CHECK     STATUS\nroot        checkout  identity  mismatch\n"
	if output.String() != want {
		t.Fatalf("identity-mismatch human status = %q, want %q", output.String(), want)
	}
}

func TestRenderWorkspaceStatusReturnsWriterFailures(t *testing.T) {
	for _, writer := range []io.Writer{
		&failingStatusWriter{err: io.ErrClosedPipe},
		&failingStatusWriter{err: io.ErrClosedPipe, succeed: 2},
	} {
		err := renderWorkspaceStatus(writer, service.WorkspaceStatus{Workspace: "feature/status", Repositories: []service.RepositoryStatus{{ID: "root", Status: "clean"}}})
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("renderWorkspaceStatus error = %v, want %v", err, io.ErrClosedPipe)
		}
	}
}

type failingStatusWriter struct {
	err     error
	succeed int
}

func (w *failingStatusWriter) Write(value []byte) (int, error) {
	if w.succeed > 0 {
		w.succeed--
		return len(value), nil
	}
	return 0, w.err
}
