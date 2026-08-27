package cli

import (
	"fmt"
	"io"

	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

func newFetchCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir, workspaceName string
	var dryRun, jsonOutput, verbose bool
	command := &cobra.Command{
		Use:   "fetch",
		Short: "refresh each configured remote-tracking reference",
		Long:  "Fetch explicitly refreshes only each checkout's configured remote-tracking reference in parent-first order. It never moves a local branch, HEAD, or worktree, and it is non-transactional: earlier successful fetches remain after a later failure.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if dataDir == "" {
				paths, _, err := resolveRuntimePaths()
				if err != nil {
					return err
				}
				dataDir = paths.DataDir
			}
			resolution, err := resolveCurrentWorkspace(command.Context(), *projectPath, dataDir)
			if err != nil {
				return err
			}
			workspace := resolution.Workspace
			if workspaceName != "" {
				workspace, err = service.RequireWorkspace(resolution.Project, dataDir, workspaceName)
				if err != nil {
					return err
				}
			}
			var streamErr error
			var streamed bool
			request := service.FetchRequest{DryRun: dryRun}
			if !jsonOutput && !dryRun {
				request.OnComplete = func(entry service.FetchRepositoryResult) error {
					streamErr = renderFetchRepository(stdout, entry, verbose)
					if streamErr == nil {
						streamed = true
					}
					return streamErr
				}
			}
			value, runErr := service.NewFetchService().Fetch(command.Context(), resolution.Project, workspace, request)
			if streamErr != nil {
				return outputFailure{streamErr}
			}
			if runErr != nil && value.Version == 0 {
				return runErr
			}
			if jsonOutput {
				if err := render.JSON(stdout, value); err != nil {
					return outputFailure{err}
				}
			} else if dryRun || (runErr != nil && !streamed) {
				if err := renderFetchResult(stdout, value, verbose); err != nil {
					return outputFailure{err}
				}
			} else if err := render.Line(stdout, fmt.Sprintf("Workspace: %s status=%s", value.Workspace, value.Status)); err != nil {
				return outputFailure{err}
			}
			if runErr != nil {
				return outputFailure{runErr}
			}
			return nil
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().StringVar(&workspaceName, "workspace", "", "workspace name")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "advertise configured refs without changing local tracking refs")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().BoolVar(&verbose, "verbose", false, "include configured remote and ref facts")
	return command
}

func renderFetchResult(stdout io.Writer, value service.FetchResult, verbose bool) error {
	if value.DryRun {
		if err := render.Line(stdout, "Dry run: no remote-tracking refs changed."); err != nil {
			return err
		}
	}
	if err := render.Line(stdout, fmt.Sprintf("Workspace: %s status=%s", value.Workspace, value.Status)); err != nil {
		return err
	}
	for _, repository := range value.Repositories {
		if err := renderFetchRepository(stdout, repository, verbose); err != nil {
			return err
		}
	}
	return nil
}

func renderFetchRepository(stdout io.Writer, repository service.FetchRepositoryResult, verbose bool) error {
	line := fmt.Sprintf("Repository: %s path=%s status=%s", repository.ID, repository.Path, repository.Status)
	if verbose {
		line += fmt.Sprintf(" remote=%s ref=%s", repository.Remote, repository.RemoteRef)
	}
	if err := render.Line(stdout, line); err != nil {
		return err
	}
	if repository.Failure != nil {
		return render.Line(stdout, fmt.Sprintf("failure: %s: %s", repository.Failure.Code, repository.Failure.Message))
	}
	return nil
}
