package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

type releaseLocker interface {
	Lock(context.Context, service.ReleaseLockRequest) (service.ReleaseLockResult, error)
}
type releaseMaterializer interface {
	Materialize(context.Context, service.ReleaseMaterializeRequest) (service.ReleaseMaterializeResult, error)
}

var newReleaseLockService = func() releaseLocker { return service.NewReleaseLockService() }
var newReleaseMaterializeService = func() releaseMaterializer { return service.NewReleaseMaterializeService() }
var resolveReleaseContext = func(command *cobra.Command, projectPath, dataDir string) (service.Resolution, error) {
	return service.NewResolver().ResolveReadOnly(command.Context(), service.ResolveRequest{Path: ".", ProjectPath: projectPath, DataDir: dataDir})
}

func newReleaseCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var jsonOutput, force, dryRun, noHooks bool
	var dataDir string
	command := &cobra.Command{Use: "release", Short: "freeze a workspace into a reproducible source lock", Long: "Compose reproducible release source from a local lock and an exact CI materialization. wtree does not commit, tag, push, publish, deploy, manage credentials, or run implicit build work.", Args: noArguments}
	command.PersistentFlags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.PersistentFlags().StringVar(&dataDir, "data-dir", "", "data directory")
	lock := &cobra.Command{Use: "lock <release-name> [workspace]", Short: "write the current non-base commits to project.wtree.lock.yml", Long: "Validate one complete clean workspace and write a deterministic local release lock. This command never fetches, commits, tags, pushes, or publishes; local post-release hooks are optional trusted caller automation.", Args: func(_ *cobra.Command, args []string) error {
		if len(args) != 1 && len(args) != 2 {
			return invalidArgumentsError{cause: fmt.Errorf("release lock requires a release name and optional workspace")}
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		if dataDir == "" {
			paths, _, err := resolveRuntimePaths()
			if err != nil {
				return err
			}
			dataDir = paths.DataDir
		}
		resolution, err := resolveReleaseContext(cmd, *projectPath, dataDir)
		if err != nil {
			return err
		}
		workspace := resolution.Workspace
		if len(args) == 2 {
			workspace, err = service.RequireWorkspace(resolution.Project, dataDir, args[1])
			if err != nil {
				return err
			}
		}
		result, err := newReleaseLockService().Lock(cmd.Context(), service.ReleaseLockRequest{Project: resolution.Project, Workspace: workspace, Name: args[0], Force: force, DryRun: dryRun, NoHooks: noHooks, DataDir: dataDir, Environment: os.Environ(), Windows: runtime.GOOS == "windows"})
		if err != nil {
			if _, setup := service.SetupIncompleteFrom(err); setup && !jsonOutput {
				if renderErr := renderReleaseLock(stdout, result); renderErr != nil {
					return outputFailure{renderErr}
				}
			}
			return err
		}
		if jsonOutput {
			if renderErr := render.JSON(stdout, result); renderErr != nil {
				return outputFailure{renderErr}
			}
		} else if renderErr := renderReleaseLock(stdout, result); renderErr != nil {
			return outputFailure{renderErr}
		}
		return nil
	}}
	lock.Flags().BoolVar(&force, "force", false, "replace an untracked or locally modified release lock")
	lock.Flags().BoolVar(&dryRun, "dry-run", false, "validate and show the lock without writing or running hooks")
	lock.Flags().BoolVar(&noHooks, "no-hooks", false, "skip trusted local post-release hooks")
	command.AddCommand(lock)
	var materializeDryRun, materializeVerbose bool
	materialize := &cobra.Command{Use: "materialize <lock-file>", Short: "reconstruct locked children around a CI base checkout", Long: "Validate the tracked portable manifest and release lock in a clean caller-provided base checkout, then fetch advertised branch and tag refs and publish exact detached child checkouts. Authentication remains Git-owned and noninteractive. This command runs no lifecycle hooks.", Args: func(_ *cobra.Command, args []string) error {
		if len(args) != 1 {
			return invalidArgumentsError{cause: fmt.Errorf("release materialize requires one lock file")}
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		if dataDir == "" {
			paths, _, err := resolveRuntimePaths()
			if err != nil {
				return err
			}
			dataDir = paths.DataDir
		}
		result, err := newReleaseMaterializeService().Materialize(cmd.Context(), service.ReleaseMaterializeRequest{LockPath: args[0], DataDir: dataDir, DryRun: materializeDryRun})
		if err != nil {
			return err
		}
		if jsonOutput {
			if renderErr := render.JSON(stdout, result); renderErr != nil {
				return outputFailure{renderErr}
			}
			return nil
		}
		if materializeVerbose {
			if renderErr := render.Line(stdout, "Release materialization: "+result.Status); renderErr != nil {
				return outputFailure{renderErr}
			}
		}
		if renderErr := renderReleaseMaterialize(stdout, result); renderErr != nil {
			return outputFailure{renderErr}
		}
		return nil
	}}
	materialize.Flags().BoolVar(&materializeDryRun, "dry-run", false, "validate and render without remote or filesystem mutation")
	materialize.Flags().BoolVar(&materializeVerbose, "verbose", false, "include materialization status")
	command.AddCommand(materialize)
	return command
}

func renderReleaseLock(out io.Writer, result service.ReleaseLockResult) error {
	for _, line := range []string{"Release lock: " + result.Status, "Release: " + result.ReleaseName, "Project: " + result.ProjectID, "Lock path: " + result.LockPath, "Manifest SHA-256: " + result.ManifestSHA256} {
		if err := render.Line(out, line); err != nil {
			return err
		}
	}
	for _, repo := range result.Repositories {
		if err := render.Line(out, fmt.Sprintf("Repository: %s %s", repo.ID, repo.Revision)); err != nil {
			return err
		}
	}
	if result.DryRun {
		return render.Line(out, "Dry run: true")
	}
	if result.HooksSkipped {
		return render.Line(out, "Hooks: skipped")
	}
	if result.HooksCompleted {
		return render.Line(out, "Hooks: completed")
	}
	if result.HookFailure != "" {
		return render.Line(out, "Hooks: failed after lock success")
	}
	return nil
}

func renderReleaseMaterialize(out io.Writer, result service.ReleaseMaterializeResult) error {
	for _, line := range []string{"Release materialize: " + result.Status, "Release: " + result.ReleaseName, "Project: " + result.ProjectID, "Lock path: " + result.LockPath, "Manifest SHA-256: " + result.ManifestSHA256} {
		if err := render.Line(out, line); err != nil {
			return err
		}
	}
	for _, repository := range result.Repositories {
		line := fmt.Sprintf("Repository: %s expected=%s", repository.ID, repository.Expected)
		if repository.Observed != "" {
			line += " observed=" + repository.Observed
		}
		if err := render.Line(out, line); err != nil {
			return err
		}
	}
	if result.DryRun {
		return render.Line(out, "Dry run: true")
	}
	return nil
}
