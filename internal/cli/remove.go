package cli

import (
	"fmt"
	"io"

	"github.com/marcel/wtree/internal/render"
	"github.com/marcel/wtree/internal/service"
	"github.com/marcel/wtree/internal/transaction"
	"github.com/spf13/cobra"
)

func newRemoveCommand(stdout, stderr io.Writer, projectPath *string) *cobra.Command {
	var dataDir string
	var dryRun, force, jsonOutput, verbose bool
	command := &cobra.Command{
		Use:   "remove <workspace>",
		Short: "remove workspace worktrees while retaining branches and state",
		Args:  exactArguments(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			project, dataDir, err := resolveWorkspaceProject(command.Context(), *projectPath, dataDir)
			if err != nil {
				return err
			}
			workspace, err := service.RequireWorkspace(project, dataDir, arguments[0])
			if err != nil {
				return err
			}
			remover := service.NewWorkspaceRemover()
			value, err := remover.PlanRemove(command.Context(), project, workspace, force)
			if err != nil {
				return err
			}
			if dryRun {
				if jsonOutput {
					return render.JSON(stdout, value)
				}
				return renderRemovalPlan(stdout, value, true)
			}
			if err := service.NewResolver().ReconcileProject(command.Context(), dataDir, project); err != nil {
				return err
			}
			var progressErr error
			progress := func(event transaction.Event) {
				if !verbose || jsonOutput || progressErr != nil {
					return
				}
				progressErr = render.Line(stderr, fmt.Sprintf("%s %s", event.Kind, event.Step))
			}
			value, err = remover.Remove(command.Context(), project, workspace, dataDir, force, progress)
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
				return render.JSON(stdout, value)
			}
			return renderRemovalPlan(stdout, value, false)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate and render without mutation")
	command.Flags().BoolVar(&force, "force", false, "allow removal of dirty worktrees only")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().BoolVar(&verbose, "verbose", false, "emit transaction progress")
	return command
}

func renderRemovalPlan(stdout io.Writer, value service.RemovalPlan, dryRun bool) error {
	if dryRun {
		if err := render.Line(stdout, "Operation: remove"); err != nil {
			return err
		}
	}
	if err := render.Line(stdout, "Workspace: "+value.WorkspaceName); err != nil {
		return err
	}
	for _, repository := range value.Repositories {
		if err := render.Line(stdout, fmt.Sprintf("remove %s  %s", repository.ID, repository.Path)); err != nil {
			return err
		}
	}
	for _, override := range value.Overrides {
		if err := render.Line(stdout, fmt.Sprintf("force override: %s: %s", override.RepositoryID, override.Reason)); err != nil {
			return err
		}
	}
	if dryRun {
		return render.Line(stdout, "No changes made.")
	}
	return render.Line(stdout, "Removed worktrees; retained branches and workspace state.")
}
