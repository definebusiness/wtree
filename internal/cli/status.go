package cli

import (
	"io"
	"strconv"

	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

func newStatusCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir string
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "status [workspace]",
		Short: "show workspace checkout and upstream status",
		Long: "Inspect every declared forest checkout in deterministic parent-first order, including working-tree and structural status alongside upstream drift. " +
			"UPSTREAM comparisons use last-fetched local upstream facts; status does not fetch or contact remotes.",
		Args: maximumOneArgument,
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
			if len(arguments) == 1 {
				workspace, err = service.RequireWorkspace(resolution.Project, dataDir, arguments[0])
				if err != nil {
					return err
				}
			}
			value, err := service.NewStatusService().Status(command.Context(), resolution.Project, workspace)
			if err != nil {
				return err
			}
			if jsonOutput {
				return render.JSON(stdout, value)
			}
			return renderWorkspaceStatus(stdout, value)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return command
}

func renderWorkspaceStatus(stdout io.Writer, value service.WorkspaceStatus) error {
	if err := render.Line(stdout, "Workspace: "+value.Workspace); err != nil {
		return err
	}
	if err := render.Line(stdout, ""); err != nil {
		return err
	}
	rows := [][]string{{"REPOSITORY", "BRANCH", "MOUNT", "STATUS", "UPSTREAM"}}
	for _, repository := range value.Repositories {
		branch := repository.Branch
		if branch == "" {
			branch = repository.ExpectedBranch
		}
		rows = append(rows, []string{repository.ID, branch, repository.Mount, repository.Status, renderUpstreamStatus(repository)})
	}
	return render.Table(stdout, rows)
}

func renderUpstreamStatus(repository service.RepositoryStatus) string {
	if repository.Missing || repository.StaleState || repository.UnknownRepository || repository.Detached {
		return "n/a"
	}
	if !repository.Upstream {
		return "none"
	}
	switch {
	case repository.Ahead == 0 && repository.Behind == 0:
		return "up-to-date"
	case repository.Behind == 0:
		return "ahead " + strconv.Itoa(repository.Ahead)
	case repository.Ahead == 0:
		return "behind " + strconv.Itoa(repository.Behind)
	default:
		return "diverged (ahead " + strconv.Itoa(repository.Ahead) + ", behind " + strconv.Itoa(repository.Behind) + ")"
	}
}
