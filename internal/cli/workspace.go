package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/transaction"
	"github.com/spf13/cobra"
)

func newCheckoutCommand(stdout, stderr io.Writer, projectPath *string) *cobra.Command {
	var targetPath, worktreeRoot, dataDir string
	var mounts []string
	var dryRun, jsonOutput, verbose, force bool
	command := &cobra.Command{
		Use:   "checkout <workspace-or-branch>",
		Short: "checkout an existing synchronized workspace branch",
		Long:  "Restore retained workspace mounts when state exists, or add worktrees for an existing branch. Checkout never creates branches.",
		Args:  exactArguments(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if force {
				return invalidArgumentsError{cause: fmt.Errorf("checkout does not support --force")}
			}
			ctx := command.Context()
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
			request := service.WorkspaceCheckoutRequest{WorkspaceName: arguments[0], TargetPath: targetPath, WorktreeRoot: worktreeRoot, DataDir: dataDir, Mounts: overrides}
			creator := service.NewWorkspaceCreator()
			value, err := creator.PlanCheckout(ctx, resolution.Project, request)
			if err != nil {
				return err
			}
			if dryRun {
				if jsonOutput {
					return render.JSON(stdout, value)
				}
				return renderWorkspacePlan(stdout, value)
			}
			if err := resolver.ReconcileProject(ctx, dataDir, resolution.Project); err != nil {
				return err
			}
			var progressErr error
			progress := func(event transaction.Event) {
				if !verbose || jsonOutput || progressErr != nil {
					return
				}
				progressErr = render.Line(stderr, fmt.Sprintf("%s %s", event.Kind, event.Step))
			}
			value, err = creator.CheckoutWorkspace(ctx, resolution.Project, request, progress)
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
			return renderCheckoutSuccess(stdout, value)
		},
	}
	command.Flags().StringVar(&targetPath, "path", "", "absolute workspace path")
	command.Flags().StringVar(&worktreeRoot, "worktree-root", "", "worktree storage root")
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().StringArrayVar(&mounts, "mount", nil, "workspace-specific repository mount in id=mount form")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate and render without mutation")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().BoolVar(&verbose, "verbose", false, "emit transaction progress")
	command.Flags().BoolVar(&force, "force", false, "unsupported for checkout")
	return command
}

func renderCheckoutSuccess(stdout io.Writer, value plan.WorkspacePlan) error {
	for _, line := range []string{"Checked out workspace: " + value.WorkspaceName, "Target: " + value.RootPath} {
		if err := render.Line(stdout, line); err != nil {
			return err
		}
	}
	return nil
}

func newListCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir string
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "list",
		Short: "list known workspaces",
		Args:  noArguments,
		RunE: func(command *cobra.Command, _ []string) error {
			project, dataDir, err := resolveWorkspaceProject(command.Context(), *projectPath, dataDir)
			if err != nil {
				return err
			}
			workspaces, err := service.ListWorkspaces(project, dataDir)
			if err != nil {
				return err
			}
			if jsonOutput {
				return render.JSON(stdout, workspaceViews(workspaces))
			}
			nameWidth := 0
			for _, workspace := range workspaces {
				if len(workspace.Name) > nameWidth {
					nameWidth = len(workspace.Name)
				}
			}
			for _, workspace := range workspaces {
				if err := render.Line(stdout, fmt.Sprintf("%-*s  %s", nameWidth, workspace.Name, workspace.RootPath)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return command
}

func newPathCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir string
	command := &cobra.Command{
		Use:   "path <workspace>",
		Short: "print one workspace path",
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
			return render.Line(stdout, workspace.RootPath)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	return command
}

func newRepoCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir string
	command := &cobra.Command{Use: "repo", Short: "inspect repositories in the current workspace", Args: noArguments}
	command.PersistentFlags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.AddCommand(
		newRepoPathCommand(stdout, projectPath, &dataDir),
		newRepoGetCommand(stdout, projectPath, &dataDir),
	)
	return command
}

func newRepoPathCommand(stdout io.Writer, projectPath *string, dataDir *string) *cobra.Command {
	return &cobra.Command{
		Use: "path <repository-id>", Short: "print one repository checkout path", Args: exactArguments(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			resolution, err := resolveCurrentWorkspace(command.Context(), *projectPath, *dataDir)
			if err != nil {
				return err
			}
			path, err := resolution.Workspace.ResolveRepository(arguments[0])
			if err != nil {
				return service.NewError(service.ErrorValidation, err)
			}
			return render.Line(stdout, path)
		},
	}
}

func newRepoGetCommand(stdout io.Writer, projectPath *string, dataDir *string) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use: "get <repository-id>", Short: "show one repository checkout", Args: exactArguments(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			resolution, err := resolveCurrentWorkspace(command.Context(), *projectPath, *dataDir)
			if err != nil {
				return err
			}
			checkout, found := checkoutByID(resolution.Workspace, arguments[0])
			if !found {
				return service.NewError(service.ErrorValidation, fmt.Errorf("workspace does not contain repository %q", arguments[0]))
			}
			if jsonOutput {
				return render.JSON(stdout, checkoutView(resolution.Workspace.Name, checkout))
			}
			return render.Line(stdout, checkout.ResolvedPath)
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return command
}

func resolveWorkspaceProject(ctx context.Context, projectPath, dataDir string) (domain.Project, string, error) {
	if dataDir == "" {
		paths, _, err := resolveRuntimePaths()
		if err != nil {
			return domain.Project{}, "", err
		}
		dataDir = paths.DataDir
	}
	project, err := service.NewResolver().ResolveProject(ctx, service.ResolveRequest{Path: ".", ProjectPath: projectPath, DataDir: dataDir})
	if err != nil {
		return domain.Project{}, "", err
	}
	return project, dataDir, nil
}

func resolveCurrentWorkspace(ctx context.Context, projectPath, dataDir string) (service.Resolution, error) {
	if dataDir == "" {
		paths, _, err := resolveRuntimePaths()
		if err != nil {
			return service.Resolution{}, err
		}
		dataDir = paths.DataDir
	}
	return service.NewResolver().ResolveReadOnly(ctx, service.ResolveRequest{Path: ".", ProjectPath: projectPath, DataDir: dataDir})
}

type workspaceView struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Path                 string   `json:"path"`
	Partial              bool     `json:"partial,omitempty"`
	MissingRepositoryIDs []string `json:"missingRepositoryIds,omitempty"`
}

func workspaceViews(workspaces []domain.Workspace) []workspaceView {
	values := make([]workspaceView, 0, len(workspaces))
	for _, workspace := range workspaces {
		values = append(values, workspaceView{ID: workspace.ID, Name: workspace.Name, Path: workspace.RootPath, Partial: workspace.Partial, MissingRepositoryIDs: workspace.MissingRepositoryIDs})
	}
	return values
}

type checkoutJSONView struct {
	ID        string `json:"id"`
	Branch    string `json:"branch,omitempty"`
	Head      string `json:"head"`
	Mount     string `json:"mount"`
	Path      string `json:"path"`
	Detached  bool   `json:"detached,omitempty"`
	Workspace string `json:"workspace"`
}

func checkoutView(workspaceName string, checkout domain.Checkout) checkoutJSONView {
	return checkoutJSONView{ID: checkout.RepositoryID, Branch: checkout.Branch, Head: checkout.Head, Mount: checkout.Mount, Path: checkout.ResolvedPath, Detached: checkout.Detached, Workspace: workspaceName}
}

func checkoutByID(workspace domain.Workspace, id string) (domain.Checkout, bool) {
	for _, checkout := range workspace.Checkouts {
		if checkout.RepositoryID == id {
			return checkout, true
		}
	}
	return domain.Checkout{}, false
}
