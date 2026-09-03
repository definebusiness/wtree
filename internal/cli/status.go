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
			"UPSTREAM comparisons use last-fetched local upstream facts. When an authoritative locally tracked manifest is available, status also reports local manifest/state/disk drift; it does not fetch or contact remotes.",
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
			value, err := service.NewStatusService().StatusWithDataDir(command.Context(), resolution.Project, workspace, dataDir)
			if err != nil {
				return err
			}
			if jsonOutput {
				if err := render.JSON(stdout, value); err != nil {
					return outputFailure{err}
				}
				return nil
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
	if err := render.Table(stdout, rows); err != nil {
		return err
	}
	if len(value.Drift) == 0 {
		return renderHookSetupStatus(stdout, value.Setup)
	}
	if err := render.Line(stdout, ""); err != nil {
		return err
	}
	if err := render.Line(stdout, "Local drift:"); err != nil {
		return err
	}
	driftRows := [][]string{{"REPOSITORY", "ORIGIN", "CHECK", "STATUS"}}
	for _, drift := range value.Drift {
		driftRows = append(driftRows, []string{drift.ID, drift.Origin, drift.Check, drift.Status})
	}
	if err := render.Table(stdout, driftRows); err != nil {
		return err
	}
	return renderHookSetupStatus(stdout, value.Setup)
}

func renderHookSetupStatus(stdout io.Writer, setup []service.HookSetupStatus) error {
	if len(setup) == 0 {
		return nil
	}
	if err := render.Line(stdout, ""); err != nil {
		return err
	}
	if err := render.Line(stdout, "Setup:"); err != nil {
		return err
	}
	rows := [][]string{{"EVENT", "STATE", "NEXT", "COMPLETED", "FAILURE"}}
	for _, entry := range setup {
		next, failure := entry.NextHookID, entry.FailureKind
		if next == "" {
			next = "-"
		}
		if failure == "" {
			failure = "-"
		}
		rows = append(rows, []string{entry.Event, entry.State, next, strconv.Itoa(entry.CompletedCount), failure})
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
