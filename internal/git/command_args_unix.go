//go:build !windows

package git

func platformGitCommandArgs(args []string) []string {
	return args
}
