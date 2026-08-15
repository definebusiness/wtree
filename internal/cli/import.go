package cli

import (
	"errors"
	"io"

	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

func newImportCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir, name string
	var allowPartial, dryRun, jsonOutput bool
	command := &cobra.Command{
		Use:   "import [path]",
		Short: "record an existing workspace without rewriting its checkouts",
		Args:  maximumOneArgument,
		RunE: func(command *cobra.Command, arguments []string) error {
			if dataDir == "" {
				paths, _, err := resolveRuntimePaths()
				if err != nil {
					return err
				}
				dataDir = paths.DataDir
			}
			path := "."
			if len(arguments) == 1 {
				path = arguments[0]
			}
			resolver := service.NewResolver()
			project, err := resolver.ResolveProject(command.Context(), service.ResolveRequest{Path: path, ProjectPath: *projectPath, DataDir: dataDir})
			if *projectPath == "" && path != "." && (errors.Is(err, service.ErrNoProjectContext) || errors.Is(err, service.ErrStaleRegistry)) {
				project, err = resolver.ResolveProject(command.Context(), service.ResolveRequest{Path: ".", DataDir: dataDir})
			}
			if err != nil {
				return err
			}
			request := service.ImportRequest{Path: path, Name: name, AllowPartial: allowPartial, DataDir: dataDir}
			importer := service.NewWorkspaceImporter()
			value, err := importer.PlanImport(command.Context(), project, request)
			if err != nil {
				return err
			}
			if dryRun {
				if jsonOutput {
					return render.JSON(stdout, value)
				}
				return renderImportPlan(stdout, value, true)
			}
			if err := resolver.ReconcileProject(command.Context(), dataDir, project); err != nil {
				return err
			}
			value, err = importer.Import(command.Context(), project, request)
			if err != nil {
				return err
			}
			if jsonOutput {
				return render.JSON(stdout, value)
			}
			return renderImportPlan(stdout, value, false)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().StringVar(&name, "name", "", "logical workspace name")
	command.Flags().BoolVar(&allowPartial, "allow-partial", false, "record explicitly missing configured repositories")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate and render without persisting state")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return command
}

func renderImportPlan(stdout io.Writer, value service.ImportPlan, dryRun bool) error {
	if dryRun {
		if err := render.Line(stdout, "Operation: import"); err != nil {
			return err
		}
	}
	if err := render.Line(stdout, "Workspace: "+value.WorkspaceName); err != nil {
		return err
	}
	rows := [][]string{{"REPOSITORY", "MOUNT", "BRANCH"}}
	for _, repository := range value.Repositories {
		branch := repository.Branch
		if repository.Detached {
			branch = "detached"
		}
		rows = append(rows, []string{repository.ID, repository.Mount, branch})
	}
	for _, id := range value.MissingRepositoryIDs {
		rows = append(rows, []string{"missing", "", id})
	}
	if err := render.Table(stdout, rows); err != nil {
		return err
	}
	if dryRun {
		return render.Line(stdout, "No changes made.")
	}
	return render.Line(stdout, "Imported workspace state.")
}
