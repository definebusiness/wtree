package cli

import (
	"fmt"
	"io"

	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

func newProjectCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "project",
		Short: "inspect globally registered projects",
		Long:  "Inspect the global project registry without resolving a workspace or changing any project data.",
		Args:  noArguments,
		RunE: func(command *cobra.Command, _ []string) error {
			if *projectPath != "" {
				return invalidArgumentsError{cause: fmt.Errorf("project does not support --project")}
			}
			return command.Help()
		},
	}
	command.AddCommand(newProjectListCommand(stdout, projectPath))
	command.AddCommand(newProjectPruneCommand(stdout, projectPath))
	command.AddCommand(newProjectUnregisterCommand(stdout, projectPath))
	return command
}

func newProjectUnregisterCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir string
	var dryRun, jsonOutput, force, verbose bool
	command := &cobra.Command{
		Use:   "unregister <project-id>",
		Short: "intentionally remove one project registration while retaining its data",
		Long:  "Remove only one registry registration. Project configuration, workspace state, recovery data, locks, repositories, and Git worktrees remain. The retained local configuration can cause a later mutating command from this project to register it again.",
		Args:  exactArguments(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if *projectPath != "" {
				return invalidArgumentsError{cause: fmt.Errorf("project unregister does not support --project")}
			}
			if command.Flags().Changed("force") {
				return invalidArgumentsError{cause: fmt.Errorf("project unregister does not support --force")}
			}
			if command.Flags().Changed("verbose") {
				return invalidArgumentsError{cause: fmt.Errorf("project unregister does not support --verbose")}
			}
			if dataDir == "" {
				paths, _, err := resolveRuntimePaths()
				if err != nil {
					return err
				}
				dataDir = paths.DataDir
			}
			remover := service.NewProjectRegistryRemovalService()
			plan, err := remover.PlanUnregister(command.Context(), dataDir, arguments[0])
			if err != nil {
				return err
			}
			if dryRun {
				if jsonOutput {
					return render.JSON(stdout, plan)
				}
				return renderProjectUnregisterDryRun(stdout, plan)
			}
			result, err := remover.Unregister(command.Context(), dataDir, plan)
			if err != nil {
				return err
			}
			if jsonOutput {
				return render.JSON(stdout, result)
			}
			return renderProjectUnregisterSuccess(stdout, result)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "plan without changing the registry")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().BoolVar(&force, "force", false, "unsupported for project unregister")
	command.Flags().BoolVar(&verbose, "verbose", false, "unsupported for project unregister")
	return command
}

func newProjectPruneCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir string
	var dryRun, jsonOutput, force, verbose bool
	command := &cobra.Command{
		Use:   "prune <project-id>",
		Short: "remove one evidence-backed stale project registration",
		Long:  "Remove only one objectively stale registry registration. It never removes project configuration, workspace state, recovery data, locks, repositories, or Git worktrees.",
		Args:  exactArguments(1),
		RunE: func(command *cobra.Command, arguments []string) error {
			if *projectPath != "" {
				return invalidArgumentsError{cause: fmt.Errorf("project prune does not support --project")}
			}
			if command.Flags().Changed("force") {
				return invalidArgumentsError{cause: fmt.Errorf("project prune does not support --force")}
			}
			if command.Flags().Changed("verbose") {
				return invalidArgumentsError{cause: fmt.Errorf("project prune does not support --verbose")}
			}
			if dataDir == "" {
				paths, _, err := resolveRuntimePaths()
				if err != nil {
					return err
				}
				dataDir = paths.DataDir
			}
			remover := service.NewProjectRegistryRemovalService()
			plan, err := remover.PlanPrune(command.Context(), dataDir, arguments[0])
			if err != nil {
				return err
			}
			if dryRun {
				if jsonOutput {
					return render.JSON(stdout, plan)
				}
				return renderProjectPruneDryRun(stdout, plan)
			}
			result, err := remover.Prune(command.Context(), dataDir, plan)
			if err != nil {
				return err
			}
			if jsonOutput {
				return render.JSON(stdout, result)
			}
			return renderProjectPruneSuccess(stdout, result)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "plan without changing the registry")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().BoolVar(&force, "force", false, "unsupported for project prune")
	command.Flags().BoolVar(&verbose, "verbose", false, "unsupported for project prune")
	return command
}

func renderProjectPruneDryRun(stdout io.Writer, plan service.ProjectRegistryRemovalPlan) error {
	if err := render.Line(stdout, fmt.Sprintf("Would remove registry registration %q (%s).", plan.ProjectID, plan.ConfigPath)); err != nil {
		return err
	}
	if err := render.Line(stdout, "Reasons: "+fmt.Sprint(plan.Reasons)); err != nil {
		return err
	}
	for _, category := range []string{"project configuration", "workspace state", "recovery data", "lock file"} {
		if err := render.Line(stdout, "Retained: "+category+"."); err != nil {
			return err
		}
	}
	return render.Line(stdout, "No changes made.")
}

func renderProjectPruneSuccess(stdout io.Writer, plan service.ProjectRegistryRemovalPlan) error {
	return render.Line(stdout, fmt.Sprintf("Removed registry registration %q only; project data was retained.", plan.ProjectID))
}

func renderProjectUnregisterDryRun(stdout io.Writer, plan service.ProjectRegistryRemovalPlan) error {
	if err := render.Line(stdout, fmt.Sprintf("Would remove registry registration %q (%s).", plan.ProjectID, plan.ConfigPath)); err != nil {
		return err
	}
	if err := render.Line(stdout, "Reasons: "+fmt.Sprint(plan.Reasons)); err != nil {
		return err
	}
	for _, category := range []string{"project configuration", "workspace state", "recovery data", "lock file"} {
		if err := render.Line(stdout, "Retained: "+category+"."); err != nil {
			return err
		}
	}
	if err := render.Line(stdout, "The retained local configuration can cause a later mutating command from this project to register it again."); err != nil {
		return err
	}
	return render.Line(stdout, "No changes made.")
}

func renderProjectUnregisterSuccess(stdout io.Writer, plan service.ProjectRegistryRemovalPlan) error {
	return render.Line(stdout, fmt.Sprintf("Removed registry registration %q only; all project data was retained. The retained local configuration can cause a later mutating command from this project to register it again.", plan.ProjectID))
}

func newProjectListCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var dataDir string
	var jsonOutput, dryRun, force, verbose bool
	command := &cobra.Command{
		Use:   "list",
		Short: "list globally registered projects and diagnostics",
		Long:  "Read the global registry and report project registration health. Listing is read-only and remains available outside a project directory.",
		Args:  noArguments,
		RunE: func(command *cobra.Command, _ []string) error {
			if *projectPath != "" {
				return invalidArgumentsError{cause: fmt.Errorf("project list does not support --project")}
			}
			if dryRun {
				return invalidArgumentsError{cause: fmt.Errorf("project list does not support --dry-run")}
			}
			if command.Flags().Changed("force") {
				return invalidArgumentsError{cause: fmt.Errorf("project list does not support --force")}
			}
			if command.Flags().Changed("verbose") {
				return invalidArgumentsError{cause: fmt.Errorf("project list does not support --verbose")}
			}
			if dataDir == "" {
				paths, _, err := resolveRuntimePaths()
				if err != nil {
					return err
				}
				dataDir = paths.DataDir
			}
			report, err := service.NewProjectInventoryService().Inventory(command.Context(), dataDir)
			if err != nil {
				return err
			}
			if jsonOutput {
				return render.JSON(stdout, report)
			}
			return renderProjectInventory(stdout, report)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "unsupported for project list")
	command.Flags().BoolVar(&force, "force", false, "unsupported for project list")
	command.Flags().BoolVar(&verbose, "verbose", false, "unsupported for project list")
	return command
}

func renderProjectInventory(stdout io.Writer, report service.ProjectInventoryReport) error {
	idWidth, nameWidth, statusWidth, prunableWidth := len("ID"), len("NAME"), len("STATUS"), len("PRUNABLE")
	for _, project := range report.Projects {
		idWidth = maxProjectInventoryWidth(idWidth, len(project.ID))
		nameWidth = maxProjectInventoryWidth(nameWidth, len(project.Name))
		statusWidth = maxProjectInventoryWidth(statusWidth, len(project.Status))
		prunableWidth = maxProjectInventoryWidth(prunableWidth, len(fmt.Sprintf("%t", project.Prunable)))
	}
	if err := render.Line(stdout, formatProjectInventoryRow(idWidth, nameWidth, statusWidth, prunableWidth, "ID", "NAME", "STATUS", "PRUNABLE", "CONFIG")); err != nil {
		return err
	}
	for _, project := range report.Projects {
		if err := render.Line(stdout, formatProjectInventoryRow(idWidth, nameWidth, statusWidth, prunableWidth, project.ID, project.Name, project.Status, fmt.Sprintf("%t", project.Prunable), project.ConfigPath)); err != nil {
			return err
		}
		for _, finding := range project.Findings {
			related := ""
			if len(finding.RelatedProjectIDs) != 0 {
				related = " (related: " + fmt.Sprint(finding.RelatedProjectIDs) + ")"
			}
			if err := render.Line(stdout, fmt.Sprintf("  %s: %s — %s%s", finding.Severity, finding.Code, finding.Message, related)); err != nil {
				return err
			}
		}
	}
	return nil
}

func maxProjectInventoryWidth(current, candidate int) int {
	if candidate > current {
		return candidate
	}
	return current
}

func formatProjectInventoryRow(idWidth, nameWidth, statusWidth, prunableWidth int, id, name, status, prunable, configPath string) string {
	return fmt.Sprintf("%-*s  %-*s  %-*s  %-*s  %s", idWidth, id, nameWidth, name, statusWidth, status, prunableWidth, prunable, configPath)
}
