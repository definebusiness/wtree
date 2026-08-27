package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

func newPushCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	return newPushCommandWithRunner(stdout, projectPath, func(ctx context.Context, project domain.Project, workspace domain.Workspace, request service.PushRequest) (service.PushResult, error) {
		return service.NewPushService().Push(ctx, project, workspace, request)
	})
}

type pushRunner func(context.Context, domain.Project, domain.Workspace, service.PushRequest) (service.PushResult, error)

func newPushCommandWithRunner(stdout io.Writer, projectPath *string, run pushRunner) *cobra.Command {
	var workspaceName string
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "push",
		Short: "report whether a complete workspace is ready for manual publication",
		Long:  "Push checks whether every selected checkout is already available at its exact configured upstream tip. It is a readiness report only: it never runs git push, fetches, creates refs or tags, or changes workspace state. Publication remains a manual or future workflow.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := command.Context().Err(); err != nil {
				return err
			}
			paths, _, err := resolveRuntimePaths()
			if err != nil {
				return err
			}
			dataDir := paths.DataDir
			resolution, err := resolveCurrentWorkspace(command.Context(), *projectPath, dataDir)
			if err != nil {
				if contextErr := command.Context().Err(); contextErr != nil {
					return contextErr
				}
				return err
			}
			workspace := resolution.Workspace
			if workspaceName != "" {
				workspace, err = service.RequireWorkspace(resolution.Project, dataDir, workspaceName)
				if err != nil {
					if contextErr := command.Context().Err(); contextErr != nil {
						return contextErr
					}
					return err
				}
			}
			var streamErr error
			request := service.PushRequest{}
			if !jsonOutput {
				request.OnComplete = func(entry service.PushRepositoryResult) error {
					streamErr = renderPushRepository(stdout, entry)
					return streamErr
				}
			}
			value, runErr := run(command.Context(), resolution.Project, workspace, request)
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
			} else {
				if err := render.Line(stdout, fmt.Sprintf("Workspace: %s status=%s", value.Workspace, value.Status)); err != nil {
					return outputFailure{err}
				}
			}
			if runErr != nil {
				return outputFailure{runErr}
			}
			return nil
		},
	}
	command.Flags().StringVar(&workspaceName, "workspace", "", "workspace name")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return command
}

func renderPushRepository(stdout io.Writer, repository service.PushRepositoryResult) error {
	if err := render.Line(stdout, fmt.Sprintf("Repository: %s status=%s", repository.ID, repository.Status)); err != nil {
		return err
	}
	for _, finding := range repository.Findings {
		if err := render.Line(stdout, "finding: "+finding.Code+": "+finding.Message); err != nil {
			return err
		}
	}
	if repository.Failure != nil {
		return render.Line(stdout, "failure: "+string(repository.Failure.Code)+": "+repository.Failure.Message)
	}
	return nil
}
