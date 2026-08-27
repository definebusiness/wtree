package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

func newExecCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir, workspaceName string
	var reverse, dryRun, jsonOutput bool
	command := &cobra.Command{
		Use:   "exec -- <executable> [argument...]",
		Short: "run one direct command in every verified workspace repository",
		Long:  "Verify every present checkout against persisted identity, branch, and commit facts before directly running one executable in deterministic repository order. exec never invokes an implicit shell: metacharacters are literal arguments. It cannot roll back effects made by the invoked program.",
		Args: func(command *cobra.Command, arguments []string) error {
			if command.ArgsLenAtDash() < 0 || len(arguments) == 0 || arguments[0] == "" {
				return invalidArgumentsError{cause: fmt.Errorf("exec requires an executable after --")}
			}
			return nil
		},
		RunE: func(command *cobra.Command, arguments []string) error {
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
			request := service.ExecRequest{
				Program: arguments[0], Args: arguments[1:], Reverse: reverse, DryRun: dryRun, Environment: os.Environ(),
			}
			if !jsonOutput && !dryRun {
				request.OnComplete = func(entry service.ExecRepositoryResult) error {
					streamErr = renderExecRepository(stdout, entry, false)
					if streamErr == nil {
						streamed = true
					}
					return streamErr
				}
			}
			value, runErr := service.NewExecService().Exec(command.Context(), resolution.Project, workspace, request)
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
			} else if dryRun {
				if err := renderExecResult(stdout, value); err != nil {
					return outputFailure{err}
				}
			} else if runErr != nil && !streamed {
				if err := renderExecResult(stdout, value); err != nil {
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
	command.Flags().BoolVar(&reverse, "reverse", false, "run child repositories before parents")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "verify and render without starting the executable")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return command
}

func renderExecResult(stdout io.Writer, value service.ExecResult) error {
	if value.DryRun {
		if err := render.Line(stdout, "Dry run: no program started."); err != nil {
			return err
		}
	}
	if err := render.Line(stdout, fmt.Sprintf("Workspace: %s", value.Workspace)); err != nil {
		return err
	}
	if err := render.Line(stdout, fmt.Sprintf("Program: %q Args: %q", value.Command.Program, value.Command.Args)); err != nil {
		return err
	}
	if err := render.Line(stdout, fmt.Sprintf("Execution order: %s", strings.Join(value.ExecutionOrder, ","))); err != nil {
		return err
	}
	for _, repository := range value.Repositories {
		if err := renderExecRepository(stdout, repository, value.DryRun); err != nil {
			return err
		}
	}
	return nil
}

func renderExecRepository(stdout io.Writer, repository service.ExecRepositoryResult, showEnvironment bool) error {
	exit := "not-started"
	if repository.ExitCode != nil {
		exit = fmt.Sprintf("%d", *repository.ExitCode)
	}
	if err := render.Line(stdout, fmt.Sprintf("Repository: %s path=%s status=%s exit=%s", repository.ID, repository.Path, repository.Status, exit)); err != nil {
		return err
	}
	if showEnvironment {
		for _, key := range []string{"WTREE_PROJECT_ID", "WTREE_WORKSPACE", "WTREE_REPOSITORY_ID", "WTREE_MOUNT", "WTREE_PATH", "WTREE_BRANCH", "WTREE_COMMIT"} {
			if err := render.Line(stdout, fmt.Sprintf("  %s=%q", key, repository.Environment[key])); err != nil {
				return err
			}
		}
	}
	if repository.Stdout != "" {
		if err := render.Line(stdout, "stdout: "+repository.Stdout); err != nil {
			return err
		}
	}
	if repository.Stderr != "" {
		if err := render.Line(stdout, "stderr: "+repository.Stderr); err != nil {
			return err
		}
	}
	if repository.Failure != nil {
		if err := render.Line(stdout, fmt.Sprintf("failure: %s: %s", repository.Failure.Code, repository.Failure.Message)); err != nil {
			return err
		}
	}
	return nil
}
