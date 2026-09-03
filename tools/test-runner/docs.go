package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var markdownLink = regexp.MustCompile(`!?\[[^]]*\]\((<[^>]+>|[^ )]+(?: [^)]*)?)\)`)

func checkDocs(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(path), ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range markdownLink.FindAllStringSubmatch(string(data), -1) {
			target := strings.TrimSpace(match[1])
			target = strings.TrimPrefix(strings.TrimSuffix(target, ">"), "<")
			target = strings.Split(target, "#")[0]
			if target == "" || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") || strings.Contains(target, "://") {
				continue
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), filepath.FromSlash(target))); err != nil {
				return fmt.Errorf("broken local Markdown link in %s: %s", filepath.ToSlash(path), target)
			}
		}
		return nil
	})
}
