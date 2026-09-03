package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

func newHooksCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var jsonOutput bool
	var dataDir string
	command := &cobra.Command{Use: "hooks", Short: "inspect and explicitly share or install workspace hooks", Long: "Inspect local, portable, and shared hook definitions. Sharing and installation are explicit consent operations; retry resumes one matching incomplete run without starting a fresh run.", Args: noArguments}
	command.PersistentFlags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.PersistentFlags().StringVar(&dataDir, "data-dir", "", "data directory")
	request := func(command *cobra.Command) (service.HookManagementRequest, error) {
		if dataDir == "" {
			paths, _, err := resolveRuntimePaths()
			if err != nil {
				return service.HookManagementRequest{}, err
			}
			dataDir = paths.DataDir
		}
		resolution, err := service.NewResolver().ResolveReadOnly(command.Context(), service.ResolveRequest{Path: ".", ProjectPath: *projectPath, DataDir: dataDir})
		if err != nil {
			return service.HookManagementRequest{}, err
		}
		return service.HookManagementRequest{Project: resolution.Project, DataDir: dataDir}, nil
	}
	command.AddCommand(newHooksListCommand(stdout, &jsonOutput, request), newHooksShareCommand(stdout, &jsonOutput, request), newHooksInstallCommand(stdout, &jsonOutput, request), newHooksRetryCommand(stdout, &jsonOutput, request))
	return command
}

type hookRetryService interface {
	Retry(context.Context, service.HookRetryRequest) (service.HookRetryResult, error)
}

var newHookRetryService = func() hookRetryService { return service.NewHookRetryService() }

func newHooksRetryCommand(stdout io.Writer, jsonOutput *bool, request hookRequestBuilder) *cobra.Command {
	return &cobra.Command{Use: "retry <workspace>", Short: "resume one incomplete hook run", Long: "Resume one matching incomplete lifecycle-hook run. Retry validates the recorded source, plan, and workspace state; it never starts a fresh run or reruns hooks recorded as completed.", Args: exactArguments(1), RunE: func(command *cobra.Command, arguments []string) error {
		value, err := request(command)
		if err != nil {
			return err
		}
		workspace, err := service.RequireWorkspace(value.Project, value.DataDir, arguments[0])
		if err != nil {
			return err
		}
		result, err := newHookRetryService().Retry(command.Context(), service.HookRetryRequest{Project: value.Project, Workspace: workspace, DataDir: value.DataDir, Environment: os.Environ(), Windows: runtime.GOOS == "windows", Sink: command.ErrOrStderr()})
		if err != nil {
			return err
		}
		if *jsonOutput {
			if err := render.JSON(stdout, result); err != nil {
				return outputFailure{err}
			}
			return nil
		}
		return renderHookRetry(stdout, result)
	}}
}

func renderHookRetry(writer io.Writer, result service.HookRetryResult) error {
	ids := "(none)"
	if len(result.CompletedHookIDs) != 0 {
		ids = strings.Join(result.CompletedHookIDs, ",")
	}
	for _, line := range []string{"Workspace: " + result.Workspace, "Event: " + result.Event, "Source: " + result.Source, "Status: " + result.Status, fmt.Sprintf("Resumed at: %d", result.ResumedAt), "Completed hook IDs: " + ids} {
		if err := render.Line(writer, line); err != nil {
			return err
		}
	}
	return nil
}

type hookRequestBuilder func(*cobra.Command) (service.HookManagementRequest, error)

func newHooksListCommand(stdout io.Writer, jsonOutput *bool, request hookRequestBuilder) *cobra.Command {
	return &cobra.Command{Use: "list", Short: "list portable, shared, and local hook definitions", Args: noArguments, RunE: func(command *cobra.Command, _ []string) error {
		value, err := request(command)
		if err != nil {
			return err
		}
		result, err := service.NewHookManagementService().List(command.Context(), value)
		if err != nil {
			return err
		}
		if *jsonOutput {
			if err := render.JSON(stdout, result); err != nil {
				return outputFailure{err}
			}
			return nil
		}
		return renderHookList(stdout, result)
	}}
}

func newHooksShareCommand(stdout io.Writer, jsonOutput *bool, request hookRequestBuilder) *cobra.Command {
	var force bool
	command := &cobra.Command{Use: "share <event>", Short: "share one local hook event in the portable manifest", Args: exactArguments(1), RunE: func(command *cobra.Command, arguments []string) error {
		value, err := request(command)
		if err != nil {
			return err
		}
		result, err := service.NewHookManagementService().Share(command.Context(), service.HookShareRequest{HookManagementRequest: value, Event: arguments[0], Force: force})
		if err != nil {
			return err
		}
		if *jsonOutput {
			if err := render.JSON(stdout, result); err != nil {
				return outputFailure{err}
			}
			return nil
		}
		return renderHookMutation(stdout, "share", result)
	}}
	command.Flags().BoolVar(&force, "force", false, "replace a conflicting shared event")
	return command
}

func newHooksInstallCommand(stdout io.Writer, jsonOutput *bool, request hookRequestBuilder) *cobra.Command {
	var force, missing bool
	command := &cobra.Command{Use: "install", Short: "install explicitly shared hook definitions locally", Args: noArguments, RunE: func(command *cobra.Command, _ []string) error {
		if force && missing {
			return invalidArgumentsError{cause: fmt.Errorf("hooks install --force and --missing are mutually exclusive")}
		}
		value, err := request(command)
		if err != nil {
			return err
		}
		result, err := service.NewHookManagementService().Install(command.Context(), service.HookInstallRequest{HookManagementRequest: value, Force: force, Missing: missing})
		if err != nil {
			return err
		}
		if *jsonOutput {
			if err := render.JSON(stdout, result); err != nil {
				return outputFailure{err}
			}
			return nil
		}
		return renderHookMutation(stdout, "install", result)
	}}
	command.Flags().BoolVar(&force, "force", false, "replace conflicting local events")
	command.Flags().BoolVar(&missing, "missing", false, "install only missing events")
	return command
}

func renderHookList(writer io.Writer, result service.HookListResult) error {
	if err := render.Line(writer, "Hooks:"); err != nil {
		return err
	}
	for _, group := range result.Groups {
		if err := render.Line(writer, "Source: "+group.Source); err != nil {
			return err
		}
		if len(group.Events) == 0 {
			if err := render.Line(writer, "  (none)"); err != nil {
				return err
			}
			continue
		}
		for _, event := range group.Events {
			comparison := "none"
			if event.Comparison != nil {
				comparison = event.Comparison.Source + ":" + event.Comparison.State
			}
			if err := render.Line(writer, fmt.Sprintf("  Event: %s comparison=%s", event.Event, comparison)); err != nil {
				return err
			}
			for _, hook := range event.Hooks {
				command, err := json.Marshal(hook.Command)
				if err != nil {
					return err
				}
				if err := render.Line(writer, fmt.Sprintf("    %s repository=%s timeout=%s policy=%s command=%s", hook.ID, hook.Repository, hook.Timeout, hook.ExecutionPolicy, command)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func renderHookMutation(writer io.Writer, operation string, result service.HookMutationResult) error {
	if err := render.Line(writer, fmt.Sprintf("Hooks %s complete (changed=%t)", operation, result.Changed)); err != nil {
		return err
	}
	for _, field := range []struct {
		name   string
		values []string
	}{{"added", result.Added}, {"replaced", result.Replaced}, {"unchanged", result.Unchanged}, {"skipped", result.Skipped}, {"conflicting", result.Conflicting}} {
		value := "none"
		if len(field.values) != 0 {
			value = strings.Join(field.values, ",")
		}
		if err := render.Line(writer, "  "+field.name+": "+value); err != nil {
			return err
		}
	}
	return nil
}
