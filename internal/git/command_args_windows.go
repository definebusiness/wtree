//go:build windows

package git

func platformGitCommandArgs(args []string) []string {
	return append([]string{"-c", "core.longpaths=true"}, args...)
}
