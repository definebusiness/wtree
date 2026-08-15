package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	configuration "github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/transaction"
	"github.com/spf13/cobra"
)

func newWorkspacePlanCommand(stdout, stderr io.Writer, projectPath *string, operation plan.Operation) *cobra.Command {
	var from, targetPath, worktreeRoot, dataDir string
	var mounts []string
	var dryRun, jsonOutput, force, verbose bool
	name := string(operation)
	short := "preflight and render a " + name + " workspace plan"
	long := "Build and validate a complete workspace plan without changing Git or workspace state. Use --dry-run to render this plan."
	if operation == plan.Create {
		short = "create a synchronized workspace"
		long = "Create branches and parent-first worktrees for every project repository, validate the resulting checkouts, then atomically persist workspace state. Use --dry-run to inspect the complete plan without mutation."
	}
	command := &cobra.Command{
		Use:   name + " <branch>",
		Short: short,
		Long:  long,
		Args:  exactArguments(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			ctx := command.Context()
			if operation == plan.Checkout && from != "HEAD" {
				return invalidArgumentsError{cause: fmt.Errorf("checkout does not support --from")}
			}
			if force {
				return invalidArgumentsError{cause: fmt.Errorf("%s does not support --force", operation)}
			}
			paths, home, err := resolveRuntimePaths()
			if err != nil {
				return err
			}
			if dataDir == "" {
				dataDir = paths.DataDir
			}
			resolver := service.NewResolver()
			resolution, err := resolver.ResolveReadOnly(ctx, service.ResolveRequest{Path: ".", ProjectPath: *projectPath, DataDir: dataDir})
			if err != nil {
				return err
			}
			if targetPath == "" {
				worktreeRoot, err = effectivePlannerWorktreeRoot(worktreeRoot, resolution.Project.ConfigPath, filepath.Join(paths.ConfigDir, "config.yml"), paths.WorktreeRoot, home)
				if err != nil {
					return err
				}
			}
			overrides, err := parseMountOverrides(mounts)
			if err != nil {
				return service.NewError(service.ErrorValidation, err)
			}
			value, err := service.NewWorkspacePlanner().Plan(ctx, resolution.Project, service.WorkspacePlanRequest{
				Operation: operation, WorkspaceName: arguments[0], From: from, Mounts: overrides,
				TargetPath: targetPath, WorktreeRoot: worktreeRoot, DataDir: dataDir,
			})
			if err != nil {
				return err
			}
			if dryRun {
				if jsonOutput {
					return render.JSON(stdout, value)
				}
				return renderWorkspacePlan(stdout, value)
			}
			if operation != plan.Create {
				return service.NewError(service.ErrorValidation, fmt.Errorf("%s execution is not available yet; use --dry-run to inspect its validated plan", operation))
			}
			if err := resolver.ReconcileProject(ctx, dataDir, resolution.Project); err != nil {
				return err
			}
			var progressErr error
			progress := func(event transaction.Event) {
				if !verbose || jsonOutput {
					return
				}
				if progressErr == nil {
					progressErr = render.Line(stderr, fmt.Sprintf("%s %s", event.Kind, event.Step))
				}
			}
			value, err = service.NewWorkspaceCreator().Create(ctx, resolution.Project, service.WorkspacePlanRequest{
				Operation: operation, WorkspaceName: arguments[0], From: from, Mounts: overrides,
				TargetPath: targetPath, WorktreeRoot: worktreeRoot, DataDir: dataDir,
			}, progress)
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
			return renderCreateSuccess(stdout, value)
		},
	}
	command.Flags().StringVar(&from, "from", "HEAD", "base ref (create only)")
	command.Flags().StringArrayVar(&mounts, "mount", nil, "workspace-specific repository mount in id=mount form")
	command.Flags().StringVar(&targetPath, "path", "", "absolute workspace path")
	command.Flags().StringVar(&worktreeRoot, "worktree-root", "", "worktree storage root")
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate and render without mutation")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().BoolVar(&verbose, "verbose", false, "emit transaction progress")
	command.Flags().BoolVar(&force, "force", false, "unsupported for create and checkout")
	return command
}

func renderCleanRollbackDiagnostic(stderr io.Writer, jsonOutput bool, err error) error {
	if jsonOutput || !service.HasCleanRollback(err) {
		return nil
	}
	return render.Line(stderr, "Rollback complete.")
}

func renderCreateSuccess(stdout io.Writer, value plan.WorkspacePlan) error {
	for _, line := range []string{"Created workspace: " + value.WorkspaceName, "Target: " + value.RootPath} {
		if err := render.Line(stdout, line); err != nil {
			return err
		}
	}
	return nil
}

func effectivePlannerWorktreeRoot(cliRoot, projectPath, globalPath, fallback, home string) (string, error) {
	project, err := configuration.ReadProjectFile(projectPath)
	if err != nil {
		return "", fmt.Errorf("read project configuration: %w", err)
	}
	global, err := configuration.ReadGlobalFile(globalPath)
	if os.IsNotExist(err) {
		global = configuration.GlobalConfig{Version: configuration.Version}
	} else if err != nil {
		return "", fmt.Errorf("read global configuration: %w", err)
	}
	return configuration.ResolveWorktreeRoot(cliRoot, project, global, fallback, home)
}

func parseMountOverrides(values []string) ([]service.MountOverride, error) {
	overrides := make([]service.MountOverride, 0, len(values))
	for _, value := range values {
		id, mount, found := strings.Cut(value, "=")
		if !found || id == "" || mount == "" {
			return nil, fmt.Errorf("mount %q must use repository=mount", value)
		}
		overrides = append(overrides, service.MountOverride{RepositoryID: id, Mount: mount})
	}
	return overrides, nil
}

func renderWorkspacePlan(stdout io.Writer, value plan.WorkspacePlan) error {
	lines := []string{
		"Operation: " + string(value.Operation),
		"Workspace: " + value.WorkspaceName,
		"Target: " + value.RootPath,
	}
	for _, line := range lines {
		if err := render.Line(stdout, line); err != nil {
			return err
		}
	}
	if err := render.Line(stdout, ""); err != nil {
		return err
	}
	rows := [][]string{{"REPOSITORY", "BASE", "BRANCH", "MOUNT", "PATH"}}
	for _, repository := range value.Repositories {
		rows = append(rows, []string{repository.ID, repository.Base, repository.Branch, repository.Mount, repository.Path})
	}
	if err := render.Table(stdout, rows); err != nil {
		return err
	}
	if err := render.Line(stdout, ""); err != nil {
		return err
	}
	return render.Line(stdout, "No changes made.")
}
