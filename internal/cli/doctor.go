package cli

import (
	"fmt"
	"io"

	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

func newDoctorCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir string
	var fix, dryRun, jsonOutput bool
	command := &cobra.Command{
		Use:   "doctor [workspace]",
		Short: "diagnose repository-forest drift and apply narrowly safe repairs",
		Long:  "Diagnose deterministic repository-forest topology, identity, mount, branch, and recovery drift. Read-only reports retain unresolved rollback visibility; --fix applies only independently verified safe repairs and leaves uncertain leftovers visible.",
		Args:  maximumOneArgument,
		RunE: func(command *cobra.Command, arguments []string) error {
			if dryRun && !fix {
				return invalidArgumentsError{cause: fmt.Errorf("doctor --dry-run requires --fix")}
			}
			if dataDir == "" {
				paths, _, err := resolveRuntimePaths()
				if err != nil {
					return err
				}
				dataDir = paths.DataDir
			}
			resolution, err := service.NewResolver().ResolveReadOnly(command.Context(), service.ResolveRequest{Path: ".", ProjectPath: *projectPath, DataDir: dataDir})
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
			doctor := service.NewDoctorService()
			var report service.DoctorReport
			if fix {
				report, err = doctor.Fix(command.Context(), resolution.Project, workspace, service.DoctorFixRequest{DataDir: dataDir, DryRun: dryRun})
			} else {
				report, err = doctor.Doctor(command.Context(), resolution.Project, workspace, dataDir)
			}
			if err != nil {
				return err
			}
			if jsonOutput {
				return render.JSON(stdout, report)
			}
			return renderDoctorReport(stdout, report)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().BoolVar(&fix, "fix", false, "apply only verified safe repairs")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "render repairs without changing state")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return command
}

func renderDoctorReport(stdout io.Writer, report service.DoctorReport) error {
	if err := render.Line(stdout, "Workspace: "+report.Workspace); err != nil {
		return err
	}
	for _, repository := range report.Repositories {
		if err := render.Line(stdout, fmt.Sprintf("Repository: %s parent=%s mount=%s path=%s status=%s identityMismatch=%t missing=%t mountMismatch=%t branchMismatch=%t headMismatch=%t", repository.ID, repository.ParentID, repository.Mount, repository.ResolvedPath, repository.Status, repository.IdentityMismatch, repository.Missing, repository.MountMismatch, repository.BranchMismatch, repository.HeadMismatch)); err != nil {
			return err
		}
	}
	if len(report.Findings) == 0 {
		if err := render.Line(stdout, "OK: no findings"); err != nil {
			return err
		}
	}
	for _, finding := range report.Findings {
		name := finding.Code
		if finding.RepositoryID != "" {
			name += " (" + finding.RepositoryID + ")"
		}
		if err := render.Line(stdout, fmt.Sprintf("%s: %s — %s", finding.Severity, name, finding.Message)); err != nil {
			return err
		}
	}
	for _, repair := range report.Repairs {
		if err := render.Line(stdout, "repair: "+repair.Message); err != nil {
			return err
		}
	}
	if report.DryRun {
		return render.Line(stdout, "No changes made.")
	}
	if report.Fixed {
		return render.Line(stdout, "Safe repairs applied.")
	}
	return nil
}
