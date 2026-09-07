package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

type companionProjectResolver func(context.Context, string, string) (domain.Project, string, error)
type companionUpdateRunner func(context.Context, service.CompanionUpdateRequest) (service.CompanionUpdateResult, error)

func newCompanionCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir string
	command := &cobra.Command{Use: "companion", Short: "operate on configured companion repositories", Args: noArguments}
	command.PersistentFlags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.AddCommand(newCompanionUpdateCommand(stdout, projectPath, &dataDir))
	return command
}

func newCompanionUpdateCommand(stdout io.Writer, projectPath, dataDir *string) *cobra.Command {
	return newCompanionUpdateCommandWithRunner(stdout, projectPath, dataDir, resolveWorkspaceProject, func(ctx context.Context, request service.CompanionUpdateRequest) (service.CompanionUpdateResult, error) {
		return service.NewCompanionUpdateService().Execute(ctx, request)
	})
}

// newCompanionUpdateCommandWithRunner keeps command-owned output tests
// deterministic. The production command still resolves and executes through
// the established service boundaries.
func newCompanionUpdateCommandWithRunner(stdout io.Writer, projectPath, dataDir *string, resolve companionProjectResolver, run companionUpdateRunner) *cobra.Command {
	var dryRun, jsonOutput bool
	command := &cobra.Command{
		Use:   "update <repository>",
		Short: "fast-forward one companion across present workspaces",
		Long:  "Fetch one configured companion baseline once, then safely fast-forward eligible present workspaces. This is distinct from `wtree update`: it never reconciles project configuration, merges, rebases, resets, forces, pushes, creates, or restores worktrees.",
		Args:  exactArguments(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			project, effectiveData, err := resolve(command.Context(), *projectPath, *dataDir)
			if err != nil {
				return renderCompanionUpdatePreResultFailure(stdout, jsonOutput, domain.Project{}, arguments[0], dryRun, err)
			}
			request := service.CompanionUpdateRequest{Project: project, DataDir: effectiveData, RepositoryID: arguments[0], DryRun: dryRun}
			var progressErr error
			if !jsonOutput && !dryRun {
				baseline := companionBaseline(project, arguments[0])
				if err := renderCompanionUpdateStart(stdout, project.ID, arguments[0], baseline, false); err != nil {
					return outputFailure{err}
				}
				request.Progress = func(entry service.CompanionUpdateEntry) error {
					progressErr = renderCompanionUpdateEntry(stdout, entry)
					return progressErr
				}
			}
			result, runErr := run(command.Context(), request)
			if progressErr != nil {
				return outputFailure{progressErr}
			}
			if runErr != nil && result.Version == 0 {
				return renderCompanionUpdatePreResultFailure(stdout, jsonOutput, project, arguments[0], dryRun, runErr)
			}
			result = companionUpdateWithFailure(result)
			if jsonOutput {
				if err := render.JSON(stdout, result); err != nil {
					return outputFailure{err}
				}
			} else if dryRun {
				if err := renderCompanionUpdate(stdout, result); err != nil {
					return outputFailure{err}
				}
			} else if err := renderCompanionUpdateFinal(stdout, result); err != nil {
				return outputFailure{err}
			}
			if runErr != nil {
				return companionUpdateOperationFailure(jsonOutput, classifyError(runErr))
			}
			if result.Status == "failed" {
				return companionUpdateOperationFailure(jsonOutput, companionUpdateResultError(result))
			}
			return nil
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "observe local facts and render without fetch or mutation")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit one companion-update JSON v1 document")
	return command
}

func renderCompanionUpdatePreResultFailure(stdout io.Writer, jsonOutput bool, project domain.Project, repository string, dryRun bool, err error) error {
	if !jsonOutput {
		return err
	}
	classified := classifyError(err)
	if renderErr := render.JSON(stdout, companionUpdateFailure(project, repository, dryRun, classified)); renderErr != nil {
		return outputFailure{renderErr}
	}
	return renderedOperationFailure{classified}
}

func companionUpdateOperationFailure(jsonOutput bool, err error) error {
	if jsonOutput {
		return renderedOperationFailure{err}
	}
	return err
}

func companionUpdateFailure(project domain.Project, repository string, dryRun bool, err error) service.CompanionUpdateResult {
	classified := classifyError(err)
	return service.CompanionUpdateResult{Version: 1, Operation: "companion-update", Status: "failed", DryRun: dryRun, ProjectID: project.ID, RepositoryID: repository, Baseline: companionBaseline(project, repository), Entries: []service.CompanionUpdateEntry{}, Failure: &service.CompanionUpdateFailure{Code: string(classified.Kind), Message: classified.Error()}}
}

func companionBaseline(project domain.Project, repository string) string {
	for _, candidate := range project.Repositories {
		if candidate.ID == repository {
			return candidate.DefaultBranch
		}
	}
	return ""
}

func companionUpdateResultError(result service.CompanionUpdateResult) error {
	if result.Failure != nil {
		kind := service.ErrorKind(result.Failure.Code)
		switch kind {
		case service.ErrorInvalidArguments, service.ErrorProjectNotFound, service.ErrorWorkspaceNotFound, service.ErrorValidation, service.ErrorGit, service.ErrorDirtyWorkspace, service.ErrorConflict, service.ErrorRollbackIncomplete, service.ErrorSetupIncomplete:
		default:
			kind = service.ErrorInternal
		}
		return service.NewError(kind, errors.New(result.Failure.Message))
	}
	for _, entry := range result.Entries {
		if entry.Status != "failed" && entry.Status != "canceled" {
			continue
		}
		kind := companionUpdateEntryErrorKind(entry)
		return service.NewError(kind, fmt.Errorf("companion %s %q: %s", entry.Kind, entry.Workspace, entry.Reason))
	}
	return service.NewError(service.ErrorInternal, errors.New("companion update failed without structured failure"))
}

func companionUpdateWithFailure(result service.CompanionUpdateResult) service.CompanionUpdateResult {
	if result.Status != "failed" || result.Failure != nil {
		return result
	}
	err := companionUpdateResultError(result)
	classified := classifyError(err)
	result.Failure = &service.CompanionUpdateFailure{Code: string(classified.Kind), Message: classified.Error()}
	return result
}

// A settled entry carries a stable fact reason rather than a low-level error.
// Preserve the public exit taxonomy without treating an ordinary partial
// operation result as a renderer failure.
func companionUpdateEntryErrorKind(entry service.CompanionUpdateEntry) service.ErrorKind {
	switch entry.Reason {
	case "dirty", "dirty-baseline":
		return service.ErrorDirtyWorkspace
	case "fetch-failed":
		return service.ErrorGit
	case "rollback-incomplete":
		return service.ErrorRollbackIncomplete
	case "invalid-state":
		return service.ErrorValidation
	case "state-publication-failed", "":
		return service.ErrorInternal
	default:
		return service.ErrorConflict
	}
}

func renderCompanionUpdate(stdout io.Writer, result service.CompanionUpdateResult) error {
	if err := renderCompanionUpdateStart(stdout, result.ProjectID, result.RepositoryID, result.Baseline, result.DryRun); err != nil {
		return err
	}
	for _, entry := range result.Entries {
		if err := renderCompanionUpdateEntry(stdout, entry); err != nil {
			return err
		}
	}
	return renderCompanionUpdateFinal(stdout, result)
}

func renderCompanionUpdateStart(stdout io.Writer, projectID, repository, baseline string, dryRun bool) error {
	return render.Line(stdout, fmt.Sprintf("Companion update: project=%s repository=%s baseline=%s dryRun=%t", projectID, repository, baseline, dryRun))
}

func renderCompanionUpdateEntry(stdout io.Writer, entry service.CompanionUpdateEntry) error {
	line := fmt.Sprintf("%s: status=%s action=%s", entry.Kind, entry.Status, entry.Action)
	if entry.Workspace != "" {
		line += " workspace=" + entry.Workspace
	}
	if entry.Branch != "" {
		line += " branch=" + entry.Branch
	}
	if entry.PreviousHEAD != "" {
		line += " previousHead=" + entry.PreviousHEAD
	}
	if entry.ResultingHEAD != "" {
		line += " resultingHead=" + entry.ResultingHEAD
	}
	if entry.Reason != "" {
		line += " reason=" + entry.Reason
	}
	if entry.Deferred {
		line += " deferred=true"
	}
	return render.Line(stdout, line)
}

func renderCompanionUpdateFinal(stdout io.Writer, result service.CompanionUpdateResult) error {
	if err := render.Line(stdout, "Companion update result: status="+result.Status); err != nil {
		return err
	}
	if result.Failure != nil {
		return render.Line(stdout, "Failure: "+result.Failure.Code+": "+result.Failure.Message)
	}
	return nil
}
