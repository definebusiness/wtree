package cli

import (
	"io"

	"github.com/marcel/wtree/internal/render"
	"github.com/marcel/wtree/internal/service"
	"github.com/spf13/cobra"
)

func newStatusCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir string
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "status [workspace]",
		Short: "show workspace checkout status",
		Args:  maximumOneArgument,
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
	rows := [][]string{{"REPOSITORY", "BRANCH", "MOUNT", "STATUS"}}
	for _, repository := range value.Repositories {
		branch := repository.Branch
		if branch == "" {
			branch = repository.ExpectedBranch
		}
		rows = append(rows, []string{repository.ID, branch, repository.Mount, repository.Status})
	}
	return render.Table(stdout, rows)
}
