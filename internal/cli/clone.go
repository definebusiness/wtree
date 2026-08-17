package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	configuration "github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/transaction"
	"github.com/spf13/cobra"
)

// cloneCommandResult is the stable public success envelope. The immutable
// plan remains available in full so automation can audit exactly what ran.
type cloneCommandResult struct {
	Version         int               `json:"version"`
	Operation       string            `json:"operation"`
	Status          string            `json:"status"`
	Project         cloneProjectView  `json:"project"`
	Destination     string            `json:"destination"`
	RepositoryCount int               `json:"repositoryCount"`
	ManifestSource  string            `json:"manifestSource"`
	Plan            service.ClonePlan `json:"plan"`
}

type cloneProjectView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func newCloneCommand(stdout, stderr io.Writer, projectPath *string) *cobra.Command {
	var worktreeRoot, dataDir string
	var dryRun, jsonOutput, verbose bool
	command := &cobra.Command{
		Use:   "clone <manifest-source> [destination]",
		Short: "clone and register a complete portable project",
		Long:  "Read a local or HTTP(S) portable manifest, resolve every declared branch to an exact commit, assemble all repositories in private staging, verify the result, then atomically publish and register the default workspace. Use --dry-run for a complete read-only plan.",
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
			request := service.ClonePlanRequest{ManifestSource: arguments[0], Destination: destination, CWD: cwd, DataDir: dataDir, WorktreeRoot: worktreeRoot}
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
			result, err := service.NewCloneExecutor().Execute(command.Context(), plan, progress)
			if err != nil {
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
				return render.JSON(stdout, cloneCommandResult{
					Version: 1, Operation: "clone", Status: "completed",
					Project:     cloneProjectView{ID: result.ProjectID, Name: plan.Project.Name},
					Destination: result.Destination, RepositoryCount: len(result.Repositories),
					ManifestSource: plan.Source.Value, Plan: plan,
				})
			}
			return renderCloneSuccess(stdout, plan, result)
		},
	}
	command.Flags().StringVar(&worktreeRoot, "worktree-root", "", "worktree storage root for the cloned project")
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate and render without mutation")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().BoolVar(&verbose, "verbose", false, "emit transaction progress")
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
		global = configuration.GlobalConfig{Version: configuration.Version}
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
	if _, err := fmt.Fprintf(stdout, "Operation: clone\nSource: %s\nDestination: %s\nProject: %s (%s)\nRepositories:\n", plan.Source.Value, plan.Destination.Path, plan.Project.Name, plan.Project.ID); err != nil {
		return err
	}
	for _, repository := range plan.Repositories {
		if _, err := fmt.Fprintf(stdout, "  %s\n    remote: %s\n    url: %s\n    branch: %s\n    merge: %s\n    mount: %s\n    exact commit: %s\n    verify: initial roots, clean checkout, no submodules, tracked manifest%s\n", repository.ID, repository.CloneRemote, repository.CloneURL, repository.LocalBranch, repository.RemoteRef, repository.Mount, repository.AdvertisedCommit, cloneParentIgnoreSuffix(repository.Parent)); err != nil {
			return err
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

func cloneParentIgnoreSuffix(parent string) string {
	if parent == "" {
		return ""
	}
	return ", committed parent ignore"
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
	return nil
}
