package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/service"
)

func TestConfigServiceConcurrentSetLeavesReadableGlobalConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	configService := service.NewConfigService()
	values := []string{"/worktrees/one", "/worktrees/two", "/worktrees/three", "/worktrees/four"}
	var group sync.WaitGroup
	errorsByCall := make(chan error, len(values))
	for _, value := range values {
		group.Add(1)
		go func(value string) {
			defer group.Done()
			_, err := configService.Set(context.Background(), service.ConfigRequest{
				Scope:               service.ConfigScopeGlobal,
				Key:                 service.ConfigKeyWorktreesRoot,
				Value:               value,
				GlobalConfigPath:    path,
				DefaultWorktreeRoot: "/default",
				Home:                "/home/test",
			})
			errorsByCall <- err
		}(value)
	}
	group.Wait()
	close(errorsByCall)
	for err := range errorsByCall {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := config.ReadGlobalFile(path)
	if err != nil {
		t.Fatalf("ReadGlobalFile() = %v", err)
	}
	if loaded.Worktrees.Root == "" {
		t.Fatal("concurrent set left an empty worktree root")
	}
}

func TestConfigServiceRejectsInvalidScopeKeyAndValue(t *testing.T) {
	configService := service.NewConfigService()
	base := service.ConfigRequest{GlobalConfigPath: filepath.Join(t.TempDir(), "config.yml"), DefaultWorktreeRoot: "/default", Home: "/home/test"}
	for _, request := range []service.ConfigRequest{
		{Scope: "other", Key: service.ConfigKeyWorktreesRoot, GlobalConfigPath: base.GlobalConfigPath, DefaultWorktreeRoot: base.DefaultWorktreeRoot, Home: base.Home},
		{Scope: service.ConfigScopeGlobal, Key: "unknown.key", GlobalConfigPath: base.GlobalConfigPath, DefaultWorktreeRoot: base.DefaultWorktreeRoot, Home: base.Home},
		{Scope: service.ConfigScopeGlobal, Key: service.ConfigKeyWorktreesRoot, Value: "  ", GlobalConfigPath: base.GlobalConfigPath, DefaultWorktreeRoot: base.DefaultWorktreeRoot, Home: base.Home},
	} {
		_, err := configService.Set(context.Background(), request)
		var application *service.Error
		if !errors.As(err, &application) || application.Kind != service.ErrorValidation {
			t.Fatalf("Set(%#v) error = %v, want validation error", request, err)
		}
	}
}

func TestConfigServiceProjectMutationsPreflightGlobalConfigBeforeWriting(t *testing.T) {
	for _, globalContents := range []string{
		"version: not-a-number\n",
		"version: 2\n",
	} {
		for _, operation := range []struct {
			name string
			run  func(*service.ConfigService, service.ConfigRequest) error
		}{
			{name: "set", run: func(configService *service.ConfigService, request service.ConfigRequest) error {
				_, err := configService.Set(context.Background(), request)
				return err
			}},
			{name: "unset", run: func(configService *service.ConfigService, request service.ConfigRequest) error {
				_, err := configService.Unset(context.Background(), request)
				return err
			}},
		} {
			t.Run(operation.name+"/"+globalContents, func(t *testing.T) {
				directory := t.TempDir()
				projectPath := filepath.Join(directory, ".wtree.yml")
				globalPath := filepath.Join(directory, "config.yml")
				projectBefore := []byte("version: 1\nproject:\n  id: project\n  name: project\nrepositories: {}\nworktrees:\n  root: /project-worktrees\n")
				if err := os.WriteFile(projectPath, projectBefore, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(globalPath, []byte(globalContents), 0o600); err != nil {
					t.Fatal(err)
				}
				request := service.ConfigRequest{
					Scope:               service.ConfigScopeProject,
					Key:                 service.ConfigKeyWorktreesRoot,
					Value:               "/updated-worktrees",
					GlobalConfigPath:    globalPath,
					ProjectConfigPath:   projectPath,
					DefaultWorktreeRoot: "/default-worktrees",
					Home:                "/home/test",
				}
				if err := operation.run(service.NewConfigService(), request); err == nil {
					t.Fatal("project config mutation succeeded with invalid global config")
				}
				projectAfter, err := os.ReadFile(projectPath)
				if err != nil {
					t.Fatal(err)
				}
				if string(projectAfter) != string(projectBefore) {
					t.Fatalf("project config changed after failed preflight:\n%s", projectAfter)
				}
			})
		}
	}
}
