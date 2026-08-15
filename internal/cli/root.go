// Package cli owns command parsing and maps parsing failures to stable errors.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"

	"github.com/definebusiness/wtree/internal/config"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/render"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Version is replaced by release builds when needed. Its development value is
// deterministic so local builds and tests have a stable identity.
var Version = "0.1.0"

// Execute runs wtree without taking process-level actions such as os.Exit.
func Execute(args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return ExecuteContext(ctx, args, stdout, stderr)
}

// ExecuteContext runs wtree with a caller-owned cancellation boundary. The
// process entry point uses a signal-aware context; tests and embedders can
// supply their own context without changing command behavior.
func ExecuteContext(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	args = preprocessConfigProjectScope(args)
	if handled, err := renderHowToIfRequested(stdout, args); handled {
		if err == nil {
			return nil
		}
		failure := service.NewError(service.ErrorInvalidArguments, err)
		if JSONRequested(args) {
			if renderErr := render.JSONError(stdout, failure); renderErr != nil {
				return renderErr
			}
		}
		return failure
	}
	if len(args) > 1 && isRootTerminalFlag(args[0]) {
		failure := classifyError(invalidArgumentsError{cause: fmt.Errorf("%s does not accept arguments", args[0])})
		if JSONRequested(args) {
			if renderErr := render.JSONError(stdout, failure); renderErr != nil {
				return classifyError(renderErr)
			}
		}
		return failure
	}

	command := NewRootCommand(stdout, stderr)
	command.SetContext(ctx)
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		err = classifyError(err)
		if JSONRequested(args) {
			if renderErr := render.JSONError(stdout, err); renderErr != nil {
				return renderErr
			}
		}
		return err
	}
	return nil
}
func JSONRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--json" || arg == "--json=true" {
			return true
		}
	}
	return false
}

func isUsageError(err error) bool {
	var invalid invalidArgumentsError
	if errors.As(err, &invalid) {
		return true
	}
	message := err.Error()
	return strings.HasPrefix(message, "unknown command ") || strings.HasPrefix(message, "unknown flag:")
}

func isRootTerminalFlag(argument string) bool {
	return argument == "--version" || argument == "-v" || argument == "--help" || argument == "-h"
}

// NewRootCommand constructs the root command and its stream configuration.
func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	var projectPath string
	command := &cobra.Command{
		Use:   "wtree",
		Short: "manage synchronized Git workspaces across nested repositories",
		Long:  "wtree can manage synchronized Git workspaces across nested repositories.",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return nil
		},
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       Version,
	}
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetVersionTemplate("wtree {{.Version}}\n")
	command.PersistentFlags().StringVarP(&projectPath, "project", "p", "", "explicit project directory or .wtree.yml path")
	command.SetHelpFunc(func(_ *cobra.Command, _ []string) {
		_, _ = fmt.Fprint(stdout, rootHelp)
	})
	commands := []*cobra.Command{
		newProjectCommand(stdout, &projectPath),
		newInitCommand(stdout, &projectPath),
		newConfigCommand(stdout, &projectPath),
		newWorkspacePlanCommand(stdout, stderr, &projectPath, plan.Create),
		newCheckoutCommand(stdout, stderr, &projectPath),
		newRemoveCommand(stdout, stderr, &projectPath),
		newDeleteCommand(stdout, stderr, &projectPath),
		newImportCommand(stdout, &projectPath),
		newDoctorCommand(stdout, &projectPath),
		newListCommand(stdout, &projectPath),
		newStatusCommand(stdout, &projectPath),
		newPathCommand(stdout, &projectPath),
		newRepoCommand(stdout, &projectPath),
	}
	for _, child := range commands {
		applyCommandDocumentation(child)
		command.AddCommand(child)
	}
	return command
}

const rootHelp = `wtree — manage synchronized Git workspaces across nested repositories

USAGE
  wtree [global options] <command> [arguments] [options]

DESCRIPTION
  wtree creates, imports, restores, inspects, and safely removes one logical
  workspace spanning independent nested Git repositories.

GLOBAL OPTIONS
  -h, --help              show this reference or command help
  --how-to                show the installed practical workflow guide
  -p, --project <path>    select a project directory or .wtree.yml explicitly
  -v, --version           print the installed version
  --json, --dry-run, --verbose, and --force are command-specific; unsupported
                          combinations are rejected rather than ignored.

COMMANDS
  project    inspect globally registered projects and manage registrations safely
  init       discover repositories and initialize a project
  import     record an existing workspace by Git identity
  create     create synchronized branches and worktrees
  checkout   restore retained workspace state or an existing branch
  list       list known workspaces
  status     inspect expected versus actual checkout state
  path       print one workspace path for shell composition
  repo       inspect or print a repository checkout path
  remove     remove worktrees while retaining branches and state
  delete     remove worktrees, branches, and retained state
  doctor     diagnose drift and apply narrowly safe repairs
  config     inspect or update global/project configuration

CONCEPTS
  project: one configured repository hierarchy.
  workspace: named checkouts across that hierarchy.
  repository: an independently versioned Git repository.
  mount: a child repository location relative to its parent checkout.
  repository identity: the common Git directory, used instead of directory names.

WORKTREE LOCATION
  Workspace locations come from --path, project/global worktrees.root, or the
  platform default. Use "wtree path <workspace>"; do not reconstruct paths.

EXAMPLES
  wtree init
  wtree project list
  wtree project prune stale-project-id --dry-run
  wtree project unregister project-id --dry-run
  wtree create feature/login
  cd "$(wtree path feature/login)"
  wtree status feature/login --json
  wtree doctor feature/login

EXIT CODES
  0 success; 1 internal/operational; 2 invalid arguments; 3 project not found;
  4 workspace not found; 5 validation; 6 Git; 7 dirty workspace; 8 conflict;
  9 rollback incomplete.

Run:
  wtree <command> --help
  wtree --how-to

for detailed command reference and practical workflows.
`

func applyCommandDocumentation(command *cobra.Command) {
	examples := map[string]string{
		"wtree project":            "  wtree project list\n  wtree project prune stale-project-id --dry-run\n  wtree project unregister project-id --dry-run\n",
		"wtree project list":       "  wtree project list\n  wtree project list --json\n",
		"wtree project prune":      "  wtree project prune stale-project-id --dry-run\n  wtree project prune stale-project-id --json\n",
		"wtree project unregister": "  wtree project unregister project-id --dry-run\n  wtree project unregister project-id --json\n",
		"wtree init":               "  wtree init\n  wtree init --dry-run\n",
		"wtree import":             "  wtree import /work/login --name feature/login\n  wtree import /work/login --dry-run --json\n",
		"wtree create":             "  wtree create feature/login\n  wtree create feature/login --from main\n  wtree create feature/login --mount backend=api --dry-run\n",
		"wtree checkout":           "  wtree checkout feature/login\n  wtree checkout feature/login --dry-run\n",
		"wtree list":               "  wtree list\n  wtree list --json\n",
		"wtree status":             "  wtree status feature/login\n  wtree status feature/login --json\n",
		"wtree path":               "  wtree path feature/login\n",
		"wtree repo":               "  wtree repo path backend\n  wtree repo get backend --json\n",
		"wtree repo path":          "  wtree repo path backend\n",
		"wtree repo get":           "  wtree repo get backend --json\n",
		"wtree remove":             "  wtree remove feature/login\n  wtree remove feature/login --dry-run\n",
		"wtree delete":             "  wtree delete feature/login\n  wtree delete feature/login --force\n",
		"wtree doctor":             "  wtree doctor feature/login\n  wtree doctor feature/login --fix --dry-run\n",
		"wtree config":             "  wtree config get worktrees.root\n  wtree config set worktrees.root /worktrees\n",
		"wtree config get":         "  wtree config get worktrees.root\n",
		"wtree config set":         "  wtree config set worktrees.root /worktrees\n",
		"wtree config unset":       "  wtree config unset worktrees.root\n",
		"wtree config list":        "  wtree config list\n",
	}
	commandPath := fullCommandPath(command)
	if example, found := examples[commandPath]; found {
		command.Example = example
	} else if command.Example == "" {
		command.Example = "  " + commandPath + " --help\n"
	}
	command.SetHelpFunc(func(current *cobra.Command, _ []string) {
		_, _ = fmt.Fprint(current.OutOrStdout(), detailedCommandHelp(current))
	})
	for _, child := range command.Commands() {
		if child.Name() != "help" {
			applyCommandDocumentation(child)
		}
	}
}

func fullCommandPath(command *cobra.Command) string {
	path := command.CommandPath()
	if path == "wtree" || strings.HasPrefix(path, "wtree ") {
		return path
	}
	return "wtree " + path
}

func detailedCommandHelp(command *cobra.Command) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s\n\nUSAGE\n  %s\n", fullCommandPath(command), command.UseLine())
	if command.Long != "" {
		fmt.Fprintf(&builder, "\nDESCRIPTION\n  %s\n", strings.ReplaceAll(command.Long, "\n", "\n  "))
	}
	builder.WriteString("\nOPTIONS\n")
	flags := 0
	command.NonInheritedFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden {
			return
		}
		flags++
		name := "--" + flag.Name
		if flag.Shorthand != "" {
			name = "-" + flag.Shorthand + ", " + name
		}
		fmt.Fprintf(&builder, "  %-26s %s\n", name, flag.Usage)
	})
	command.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden || !showInheritedFlag(command, flag) {
			return
		}
		flags++
		name := "--" + flag.Name
		if flag.Shorthand != "" {
			name = "-" + flag.Shorthand + ", " + name
		}
		fmt.Fprintf(&builder, "  %-26s %s\n", name, flag.Usage)
	})
	if flags == 0 {
		builder.WriteString("  (none)\n")
	}
	if isConfigCommand(command) {
		builder.WriteString(`
CONFIGURATION PROJECT SELECTION
  Place the global project selector before config:
    wtree --project <path> config get <key>
  A bare --project after config selects project-scoped configuration:
    wtree config get <key> --project
  Both forms may be combined:
    wtree --project <path> config get <key> --project
`)
	}
	builder.WriteString(`
SAFETY AND OUTPUT
  --dry-run, where listed, validates and renders without mutation. --json,
  where listed, emits one machine-readable value on stdout. --verbose emits
  operational progress only on stderr and never prints environment values.
  --force is accepted only where listed and reports each overridden safeguard.
  Unsupported options are rejected; path commands keep stdout to the path.

EXIT CODES
  0 success; 1 internal/operational; 2 invalid arguments; 3 project not found;
  4 workspace not found; 5 validation; 6 Git; 7 dirty workspace; 8 conflict;
  9 rollback incomplete.
`)
	if command.Example != "" {
		fmt.Fprintf(&builder, "\nEXAMPLES\n%s", command.Example)
	}
	return builder.String()
}

func showInheritedFlag(command *cobra.Command, flag *pflag.Flag) bool {
	if flag.Name != "project" {
		return true
	}
	return command.Name() != "init" && !isConfigCommand(command) && !isProjectCommand(command)
}

func isConfigCommand(command *cobra.Command) bool {
	return command.Name() == "config" || (command.Parent() != nil && command.Parent().Name() == "config")
}

func isProjectCommand(command *cobra.Command) bool {
	return command.Name() == "project" || (command.Parent() != nil && command.Parent().Name() == "project")
}

func newInitCommand(stdout io.Writer, projectPath *string) *cobra.Command {
	var worktreeRoot, dataDir string
	var dryRun, jsonOutput bool
	var ignores []string
	command := &cobra.Command{
		Use:   "init [path]",
		Short: "initialize a project from independent nested Git repositories",
		Long:  "Discover the root and independent nested Git repositories, write .wtree.yml, and register the project.\n\nThe original source checkout is recorded as the default workspace. Use --dry-run to preflight without writing state. If a missing local configuration is already registered by path or repository identity, init refuses the duplicate and directs you to `wtree project list` for inspection and explicit registry cleanup.",
		Args:  maximumOneArgument,
		RunE: func(command *cobra.Command, args []string) error {
			if *projectPath != "" {
				return invalidArgumentsError{cause: fmt.Errorf("init does not support --project")}
			}
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			if dataDir == "" {
				home, _ := os.UserHomeDir()
				paths, resolveErr := config.ResolvePaths(runtime.GOOS, home, environmentMap(os.Environ()))
				if resolveErr != nil {
					return resolveErr
				}
				dataDir = paths.DataDir
			}
			result, err := service.NewInitializer().Init(command.Context(), service.InitRequest{Path: path, DataDir: dataDir, WorktreeRoot: worktreeRoot, DryRun: dryRun, Ignores: ignores})
			if err != nil {
				return err
			}
			if jsonOutput {
				_, err = fmt.Fprintf(stdout, "{\"projectId\":%q,\"dryRun\":%t}\n", result.ProjectID, result.DryRun)
			} else {
				_, err = fmt.Fprintf(stdout, "initialized %s\n", result.ProjectID)
			}
			return err
		},
	}
	command.Flags().StringVar(&worktreeRoot, "worktree-root", "", "worktree storage root")
	command.Flags().StringVar(&dataDir, "data-dir", "", "data directory")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "plan without writing")
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().StringSliceVar(&ignores, "ignore", nil, "repository discovery ignore glob")
	return command
}

func maximumOneArgument(_ *cobra.Command, arguments []string) error {
	if len(arguments) > 1 {
		return invalidArgumentsError{cause: fmt.Errorf("accepts at most 1 argument, received %d", len(arguments))}
	}
	return nil
}

func exactArguments(count int) cobra.PositionalArgs {
	return func(_ *cobra.Command, arguments []string) error {
		if len(arguments) != count {
			return invalidArgumentsError{cause: fmt.Errorf("accepts exactly %d argument(s), received %d", count, len(arguments))}
		}
		return nil
	}
}

func noArguments(command *cobra.Command, arguments []string) error {
	return exactArguments(0)(command, arguments)
}

// preprocessConfigProjectScope preserves root --project <path> while making a
// bare --project after the config command the documented project-scope marker.
func preprocessConfigProjectScope(arguments []string) []string {
	processed := append([]string(nil), arguments...)
	configIndex := -1
	for index, argument := range processed {
		if argument == "config" {
			configIndex = index
			break
		}
	}
	if configIndex == -1 {
		return processed
	}
	for index := configIndex + 1; index < len(processed); index++ {
		if processed[index] == "--project" {
			processed[index] = "--project-scope"
		}
	}
	return processed
}

func environmentMap(entries []string) map[string]string {
	environment := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		environment[key] = value
		switch strings.ToUpper(key) {
		case "APPDATA":
			environment["AppData"] = value
		case "LOCALAPPDATA":
			environment["LocalAppData"] = value
		}
	}
	return environment
}

type invalidArgumentsError struct {
	cause error
}

func (e invalidArgumentsError) Error() string {
	return e.cause.Error()
}

func (e invalidArgumentsError) Unwrap() error {
	return e.cause
}

// classifyError is the single CLI boundary that turns legacy causes into the
// stable application taxonomy used by both JSON rendering and process exits.
func classifyError(err error) *service.Error {
	var application *service.Error
	if errors.As(err, &application) {
		return application
	}
	var invalid invalidArgumentsError
	if errors.As(err, &invalid) {
		return service.NewError(service.ErrorInvalidArguments, err)
	}
	if isUsageError(err) {
		return service.NewError(service.ErrorInvalidArguments, err)
	}
	if errors.Is(err, service.ErrNoProjectContext) || errors.Is(err, service.ErrAmbiguousProject) || errors.Is(err, service.ErrStaleRegistry) {
		return service.NewError(service.ErrorProjectNotFound, err)
	}
	if errors.Is(err, service.ErrAlreadyInitialized) {
		return service.NewError(service.ErrorConflict, err)
	}
	var gitError *gitadapter.Error
	if errors.As(err, &gitError) {
		return service.NewError(service.ErrorGit, err)
	}
	return service.NewError(service.ErrorInternal, err)
}

// ExitCode translates CLI errors to the stable process exit taxonomy.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	switch classifyError(err).Kind {
	case service.ErrorInvalidArguments:
		return 2
	case service.ErrorProjectNotFound:
		return 3
	case service.ErrorWorkspaceNotFound:
		return 4
	case service.ErrorValidation:
		return 5
	case service.ErrorGit:
		return 6
	case service.ErrorDirtyWorkspace:
		return 7
	case service.ErrorConflict:
		return 8
	case service.ErrorRollbackIncomplete:
		return 9
	default:
		return 1
	}
}
