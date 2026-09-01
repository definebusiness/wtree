package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	configuration "github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/transaction"
	"github.com/spf13/cobra"
)

// cloneCommandResult is the stable public success envelope. The plan records
// preflight observations; repositories records what execution actually checked
// out in deterministic repository-ID order.
type cloneCommandResult struct {
	Version          int                        `json:"version"`
	Operation        string                     `json:"operation"`
	Status           string                     `json:"status"`
	Project          cloneProjectView           `json:"project"`
	Destination      string                     `json:"destination"`
	LogicalRoot      string                     `json:"logicalRoot"`
	BaseRepository   string                     `json:"baseRepository"`
	RepositoryCount  int                        `json:"repositoryCount"`
	ManifestSource   string                     `json:"manifestSource"`
	Repositories     []cloneCompletedRepository `json:"repositories"`
	Plan             service.ClonePlan          `json:"plan"`
	HooksCompleted   *bool                      `json:"hooksCompleted,omitempty"`
	HooksSkipped     *bool                      `json:"hooksSkipped,omitempty"`
	CompletedHookIDs []string                   `json:"completedHookIds,omitempty"`
}

type cloneCompletedRepository struct {
	ID           string `json:"id"`
	ActualCommit string `json:"actualCommit"`
	Mount        string `json:"mount"`
	ResolvedPath string `json:"resolvedPath"`
}

type cloneProjectView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cloneLifecycleService interface {
	Clone(context.Context, service.CloneLifecycleRequest) (service.CloneLifecycleResult, error)
}

var newCloneLifecycleCoordinator = func() cloneLifecycleService {
	return service.NewCloneLifecycleCoordinator()
}

func newCloneCommand(stdout, stderr io.Writer, projectPath *string) *cobra.Command {
	var worktreeRoot, dataDir string
	var dryRun, jsonOutput, verbose, runHooks bool
	command := &cobra.Command{
		Use:   "clone <manifest-source> [destination]",
		Short: "clone and register a complete portable project",
		Long:  "Read a local or HTTP(S) portable manifest, observe every declared remote branch, assemble the complete repository forest in private staging from execution-time selected branch tips, verify it, then atomically publish and register the default workspace. JSON includes the logical root, designated base repository, and resolved repository topology. Use --dry-run for a complete read-only plan.",
		Args:  cloneArguments,
		RunE: func(command *cobra.Command, arguments []string) error {
			// A root-scoped project has no meaning before the new project exists.
			// Check it before runtime resolution or manifest/network access.
			if *projectPath != "" {
				return invalidArgumentsError{cause: fmt.Errorf("clone does not support --project")}
			}
			paths, home, err := resolveRuntimePaths()
			if err != nil {
				return err
			}
			if dataDir == "" {
				dataDir = paths.DataDir
			}
			worktreeRoot, err = effectiveCloneWorktreeRoot(worktreeRoot, filepath.Join(paths.ConfigDir, "config.yml"), paths.WorktreeRoot, home)
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return service.NewError(service.ErrorInternal, fmt.Errorf("resolve caller working directory: %w", err))
			}
			destination := ""
			if len(arguments) == 2 {
				destination = arguments[1]
			}
			request := service.ClonePlanRequest{ManifestSource: arguments[0], Destination: destination, CWD: cwd, DataDir: dataDir, WorktreeRoot: worktreeRoot, RunHooks: runHooks}
			planner := service.NewClonePlanner()
			plan, err := planner.Plan(command.Context(), request)
			if err != nil {
				return err
			}
			if dryRun {
				if jsonOutput {
					result, resultErr := service.NewCloneResult(plan)
					if resultErr != nil {
						return service.NewError(service.ErrorInternal, resultErr)
					}
					return render.JSON(stdout, result)
				}
				return renderClonePlan(stdout, plan)
			}

			var progressErr error
			progress := func(event transaction.Event) {
				if !verbose || jsonOutput || progressErr != nil {
					return
				}
				progressErr = render.Line(stderr, fmt.Sprintf("%s %s", event.Kind, event.Step))
			}
			lifecycle, err := newCloneLifecycleCoordinator().Clone(command.Context(), service.CloneLifecycleRequest{Plan: plan, RunHooks: runHooks, Environment: os.Environ(), Windows: runtime.GOOS == "windows", Sink: stderr, Progress: progress})
			result := lifecycle.Core
			if err != nil {
				if _, setup := service.SetupIncompleteFrom(err); setup {
					if !jsonOutput {
						if renderErr := renderCloneSuccess(stdout, plan, result); renderErr != nil {
							return renderErr
						}
					}
					return err
				}
				if progressErr == nil {
					progressErr = renderCleanRollbackDiagnostic(stderr, jsonOutput, err)
				}
				if progressErr != nil {
					return progressErr
				}
				return err
			}
			if progressErr != nil {
				return progressErr
			}
			if jsonOutput {
				var hooksCompleted, hooksSkipped *bool
				if lifecycle.HooksApplicable {
					completed, skipped := lifecycle.HooksCompleted, lifecycle.HooksSkipped
					hooksCompleted, hooksSkipped = &completed, &skipped
				}
				return render.JSON(stdout, cloneCommandResult{
					Version: 2, Operation: "clone", Status: "completed",
					Project:     cloneProjectView{ID: result.ProjectID, Name: plan.Project.Name},
					Destination: result.Destination, RepositoryCount: len(result.Repositories),
					LogicalRoot: result.LogicalRoot, BaseRepository: result.BaseRepository,
					ManifestSource: plan.Source.Value, Repositories: completedCloneRepositories(result), Plan: plan,
					HooksCompleted: hooksCompleted, HooksSkipped: hooksSkipped, CompletedHookIDs: append([]string(nil), lifecycle.CompletedHookIDs...),
				})
			}
			if err := renderCloneSuccess(stdout, plan, result); err != nil {
				return err
			}
			if lifecycle.HooksSkipped {
				return render.Line(stdout, "Portable hooks: skipped (use --run-hooks to authorize)")
			}
			if lifecycle.HooksCompleted {
				return render.Line(stdout, "Portable hooks: completed")
			}
			return nil
		},
	}
	command.Flags().StringVar(&worktreeRoot, "worktree-root", "", "worktree storage root for the cloned project")
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate and render without mutation")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().BoolVar(&verbose, "verbose", false, "emit transaction progress")
	command.Flags().BoolVar(&runHooks, "run-hooks", false, "run portable post-clone hooks for this invocation")
	return command
}

func cloneArguments(_ *cobra.Command, arguments []string) error {
	if len(arguments) < 1 || len(arguments) > 2 {
		return invalidArgumentsError{cause: fmt.Errorf("accepts 1 or 2 argument(s), received %d", len(arguments))}
	}
	return nil
}

func effectiveCloneWorktreeRoot(cliRoot, globalPath, fallback, home string) (string, error) {
	global, err := configuration.ReadGlobalFile(globalPath)
	if os.IsNotExist(err) {
		global = configuration.GlobalConfig{Version: configuration.GlobalConfigVersion}
	} else if err != nil {
		return "", fmt.Errorf("read global configuration: %w", err)
	}
	root, err := configuration.ResolveWorktreeRoot(cliRoot, configuration.ProjectConfig{}, global, fallback, home)
	if err != nil {
		return "", fmt.Errorf("resolve clone worktree root: %w", err)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve clone worktree root: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func renderClonePlan(stdout io.Writer, plan service.ClonePlan) error {
	if _, err := fmt.Fprintf(stdout, "Operation: clone\nSource: %s\nDestination: %s\nLogical root: %s\nBase repository: %s\nProject: %s (%s)\nRepositories:\n", plan.Source.Value, plan.Destination.Path, plan.LogicalRoot, plan.BaseRepository, plan.Project.Name, plan.Project.ID); err != nil {
		return err
	}
	for _, repository := range plan.Repositories {
		if _, err := fmt.Fprintf(stdout, "  %s\n    remote: %s\n    url: %s\n    branch: %s\n    merge: %s\n    mount: %s\n    observed commit: %s\n    verify: %s\n", repository.ID, repository.CloneRemote, repository.CloneURL, repository.LocalBranch, repository.RemoteRef, repository.Mount, repository.ObservedCommit, cloneVerificationSummary(repository.Verification)); err != nil {
			return err
		}
	}
	if len(plan.Hooks) != 0 {
		if _, err := fmt.Fprintln(stdout, "Hooks:"); err != nil {
			return err
		}
		for _, hook := range plan.Hooks {
			if _, err := fmt.Fprintf(stdout, "  %s/%s (%s)\n    repository: %s\n    working directory: %s\n    executable: %s\n    availability: %s\n    timeout: %s\n    policy: %s\n", hook.Source, hook.ID, hook.Event, hook.Repository, hook.WorkingDirectory, hook.ConfiguredExecutable, hook.Availability, hook.Timeout, hook.ExecutionPolicy); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(stdout, "Actions:"); err != nil {
		return err
	}
	for _, action := range plan.Actions {
		if _, err := fmt.Fprintf(stdout, "  %d. %s", action.Sequence, action.Action); err != nil {
			return err
		}
		if action.RepositoryID != "" {
			if _, err := fmt.Fprintf(stdout, " (%s)", action.RepositoryID); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return err
		}
	}
	return render.Line(stdout, "No changes made.")
}

func cloneVerificationSummary(verification service.CloneVerification) string {
	parts := []string{"initial roots", "clean checkout", "no submodules"}
	if verification.TrackedManifestExact {
		parts = append(parts, "tracked manifest")
	}
	if verification.CommittedParentIgnore {
		parts = append(parts, "committed parent ignore")
	}
	return strings.Join(parts, ", ")
}

func renderCloneSuccess(stdout io.Writer, plan service.ClonePlan, result service.CloneExecutionResult) error {
	for _, line := range []string{
		"Cloned project: " + plan.Project.Name + " (" + result.ProjectID + ")",
		"Destination: " + result.Destination,
		fmt.Sprintf("Repositories: %d", len(result.Repositories)),
		"Manifest source: " + plan.Source.Value,
	} {
		if err := render.Line(stdout, line); err != nil {
			return err
		}
	}
	for _, repository := range completedCloneRepositories(result) {
		if err := render.Line(stdout, fmt.Sprintf("Actual checkout: %s %s", repository.ID, repository.ActualCommit)); err != nil {
			return err
		}
	}
	return nil
}

func completedCloneRepositories(result service.CloneExecutionResult) []cloneCompletedRepository {
	ids := result.RepositoryIDs()
	repositories := make([]cloneCompletedRepository, 0, len(ids))
	for _, id := range ids {
		checkout := result.Repositories[id]
		repositories = append(repositories, cloneCompletedRepository{ID: id, ActualCommit: checkout.Head, Mount: checkout.Mount, ResolvedPath: checkout.ResolvedPath})
	}
	return repositories
}
