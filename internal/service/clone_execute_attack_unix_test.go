//go:build !windows

package service

func cloneGroupingReplacementRefused(error) bool { return false }

func cloneTestLogicalRoot(candidate, _ string) string { return candidate }
