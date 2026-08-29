//go:build !windows

package service

func cloneGroupingReplacementRefused(error) bool { return false }
