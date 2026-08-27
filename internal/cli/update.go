package cli

import (
	"fmt"
	"io"

	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

func newUpdateCommand(stdout, stderr io.Writer, projectPath *string) *cobra.Command {
	var dataDir, from string
	var dryRun, jsonOutput, verbose bool
	command := &cobra.Command{
		Use:   "update",
		Short: "safely reconcile a project with its portable manifest",
		Long:  "Capture a complete update snapshot. With --dry-run, render the validated plan without mutation. Without it, wtree applies only safe fast-forwards and additions, publishes matching local state transactionally, retains removed checkouts unmanaged, and never relocates or deletes an existing checkout.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
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
			defaultWorkspace, err := service.RequireWorkspace(resolution.Project, dataDir, "default")
			if err != nil {
				return err
			}
			if !dryRun {
				if verbose && !jsonOutput {
					if err := render.Line(stderr, "Applying update transaction."); err != nil {
						return err
					}
				}
				result, err := service.ExecuteUpdate(command.Context(), resolution.Project, defaultWorkspace, dataDir, from)
				if err != nil {
					return err
				}
				if jsonOutput {
					if err := render.JSON(stdout, result); err != nil {
						return outputFailure{err}
					}
					return nil
				}
				return renderUpdateSuccess(stdout, result)
			}
			snapshot, source, err := service.CollectUpdateSnapshot(command.Context(), resolution.Project, defaultWorkspace, dataDir, from, nil)
			if err != nil {
				return err
			}
			plan, err := service.BuildUpdatePlan(snapshot, source)
			if err != nil {
				return err
			}
			if verbose && !jsonOutput {
				if err := render.Line(stderr, "Captured one immutable update preflight snapshot."); err != nil {
					return err
				}
			}
			if jsonOutput {
				if err := render.JSON(stdout, plan); err != nil {
					return outputFailure{err}
				}
				return nil
			}
			return renderUpdatePlan(stdout, plan)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().StringVar(&from, "from", "", "portable manifest source overriding manifest.source for this update")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate and render without mutation")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().BoolVar(&verbose, "verbose", false, "emit read-only preflight progress")
	return command
}

func renderUpdateSuccess(stdout io.Writer, result service.UpdatePublicationResult) error {
	if _, err := fmt.Fprintln(stdout, "Update complete:"); err != nil {
		return err
	}
	for _, repository := range result.Repositories {
		if _, err := fmt.Fprintf(stdout, "  %s: %s (%s) status=%s mount=%s path=%s", repository.ID, repository.Classification, repository.Action, repository.Status, repository.Mount, repository.Path); err != nil {
			return err
		}
		if repository.Branch != "" {
			if _, err := fmt.Fprintf(stdout, " branch=%s", repository.Branch); err != nil {
				return err
			}
		}
		if repository.ActualHead != "" {
			if _, err := fmt.Fprintf(stdout, " head=%s", repository.ActualHead); err != nil {
				return err
			}
		}
		if repository.RollbackStatus != "" {
			if _, err := fmt.Fprintf(stdout, " rollback=%s", repository.RollbackStatus); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return err
		}
	}
	return nil
}

func renderUpdatePlan(stdout io.Writer, plan service.UpdatePlan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "Operation: update\nSource: %s\nCandidate digest: %s\nRepositories:\n", plan.Source.Value, plan.Source.SHA256); err != nil {
		return err
	}
	for _, repository := range plan.Repositories() {
		if _, err := fmt.Fprintf(stdout, "  %s: %s\n", repository.ID, repository.Classification); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(stdout, "Actions:"); err != nil {
		return err
	}
	for _, action := range plan.Actions() {
		if _, err := fmt.Fprintf(stdout, "  %d. %s (%s)\n", action.Sequence, action.Action, action.RepositoryID); err != nil {
			return err
		}
	}
	return render.Line(stdout, "No changes made. Dry run performs no mutation; existing mounts are not relocated and removed repositories are retained unmanaged.")
}
