package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	configuration "github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
)

func newConfigCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var projectScope, jsonOutput bool
	command := &cobra.Command{
		Use:   "config",
		Short: "inspect and update global or project configuration",
		Long: "Inspect and update configuration. By default config changes the global user configuration. " +
			"Use `config <command> --project` for project scope; root project selection remains `wtree --project <path> config ...`.",
		Args: noArguments,
	}
	command.PersistentFlags().BoolVar(&projectScope, "project-scope", false, "project configuration scope")
	_ = command.PersistentFlags().MarkHidden("project-scope")
	command.PersistentFlags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	request := func(key, value string) (service.ConfigRequest, error) {
		paths, home, err := resolveRuntimePaths()
		if err != nil {
			return service.ConfigRequest{}, err
		}
		result := service.ConfigRequest{
			Scope:               service.ConfigScopeGlobal,
			Key:                 key,
			Value:               value,
			GlobalConfigPath:    filepath.Join(paths.ConfigDir, "config.yml"),
			DefaultWorktreeRoot: paths.WorktreeRoot,
			Home:                home,
		}
		if !projectScope {
			return result, nil
		}
		resolution, err := service.NewResolver().ResolveReadOnly(context.Background(), service.ResolveRequest{Path: ".", ProjectPath: *projectPath, DataDir: paths.DataDir})
		if err != nil {
			return service.ConfigRequest{}, err
		}
		result.Scope = service.ConfigScopeProject
		result.ProjectConfigPath = resolution.Project.ConfigPath
		return result, nil
	}
	command.AddCommand(
		newConfigGetCommand(stdout, &jsonOutput, request),
		newConfigSetCommand(stdout, &jsonOutput, request),
		newConfigUnsetCommand(stdout, &jsonOutput, request),
		newConfigListCommand(stdout, &jsonOutput, request),
	)
	return command
}

type configRequestBuilder func(key, value string) (service.ConfigRequest, error)

func newConfigGetCommand(stdout io.Writer, jsonOutput *bool, build configRequestBuilder) *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "print an effective configuration value",
		Args:  exactArguments(1),
		RunE: func(_ *cobra.Command, arguments []string) error {
			request, err := build(arguments[0], "")
			if err != nil {
				return err
			}
			value, err := service.NewConfigService().Get(context.Background(), request)
			if err != nil {
				return err
			}
			if *jsonOutput {
				return render.JSON(stdout, value)
			}
			return render.Line(stdout, value.Value)
		},
	}
}

func newConfigSetCommand(stdout io.Writer, jsonOutput *bool, build configRequestBuilder) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "set a configuration value",
		Args:  exactArguments(2),
		RunE: func(_ *cobra.Command, arguments []string) error {
			request, err := build(arguments[0], arguments[1])
			if err != nil {
				return err
			}
			value, err := service.NewConfigService().Set(context.Background(), request)
			if err != nil {
				return err
			}
			return renderConfigValue(stdout, *jsonOutput, value, false)
		},
	}
}

func newConfigUnsetCommand(stdout io.Writer, jsonOutput *bool, build configRequestBuilder) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "remove a configuration override",
		Args:  exactArguments(1),
		RunE: func(_ *cobra.Command, arguments []string) error {
			request, err := build(arguments[0], "")
			if err != nil {
				return err
			}
			value, err := service.NewConfigService().Unset(context.Background(), request)
			if err != nil {
				return err
			}
			return renderConfigValue(stdout, *jsonOutput, value, false)
		},
	}
}

func newConfigListCommand(stdout io.Writer, jsonOutput *bool, build configRequestBuilder) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list effective configuration values",
		Args:  noArguments,
		RunE: func(_ *cobra.Command, _ []string) error {
			request, err := build(service.ConfigKeyWorktreesRoot, "")
			if err != nil {
				return err
			}
			values, err := service.NewConfigService().List(context.Background(), request)
			if err != nil {
				return err
			}
			if *jsonOutput {
				return render.JSON(stdout, values)
			}
			for _, value := range values {
				if err := render.Line(stdout, fmt.Sprintf("%s = %s (%s)", value.Key, value.Value, value.Source)); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func renderConfigValue(stdout io.Writer, jsonOutput bool, value service.ConfigValue, scalar bool) error {
	if jsonOutput {
		return render.JSON(stdout, value)
	}
	if scalar {
		return render.Line(stdout, value.Value)
	}
	return render.Line(stdout, fmt.Sprintf("%s = %s", value.Key, value.Value))
}

func resolveRuntimePaths() (configuration.Paths, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return configuration.Paths{}, "", err
	}
	paths, err := configuration.ResolvePaths(runtime.GOOS, home, environmentMap(os.Environ()))
	if err != nil {
		return configuration.Paths{}, "", err
	}
	return paths, home, nil
}
