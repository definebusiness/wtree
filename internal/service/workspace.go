package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/store"
	"github.com/definebusiness/wtree/internal/transaction"
)

// WorkspaceCheckoutRequest contains the checkout-specific inputs. If state
// already exists for the workspace, its root and mounts are restored unless a
// caller explicitly supplies a target path or mount override.
type WorkspaceCheckoutRequest struct {
	WorkspaceName string
	TargetPath    string
	WorktreeRoot  string
	DataDir       string
	Mounts        []MountOverride
}

// PlanCheckout resolves retained workspace state before planning existing
// branches. It never creates branches and is safe to use for dry-run output.
func (c *WorkspaceCreator) PlanCheckout(ctx context.Context, project domain.Project, request WorkspaceCheckoutRequest) (plan.WorkspacePlan, error) {
	planRequest, err := prepareCheckoutRequest(project, request)
	if err != nil {
		return plan.WorkspacePlan{}, err
	}
	return c.planner.Plan(ctx, project, planRequest)
}

// CheckoutWorkspace restores a workspace from retained state or creates an
// unambiguous existing-branch checkout using only add-worktree effects.
func (c *WorkspaceCreator) CheckoutWorkspace(ctx context.Context, project domain.Project, request WorkspaceCheckoutRequest, progress func(transaction.Event)) (plan.WorkspacePlan, error) {
	planRequest, err := prepareCheckoutRequest(project, request)
	if err != nil {
		return plan.WorkspacePlan{}, err
	}
	return c.Checkout(ctx, project, planRequest, progress)
}

func prepareCheckoutRequest(project domain.Project, request WorkspaceCheckoutRequest) (WorkspacePlanRequest, error) {
	if request.WorkspaceName == "" {
		return WorkspacePlanRequest{}, NewError(ErrorValidation, errors.New("workspace name is required"))
	}
	stored, found, err := FindWorkspace(project, request.DataDir, request.WorkspaceName)
	if err != nil {
		return WorkspacePlanRequest{}, err
	}
	mounts := request.Mounts
	target := request.TargetPath
	if found {
		if stored.Partial {
			return WorkspacePlanRequest{}, NewError(ErrorValidation, fmt.Errorf("workspace %q is partial and cannot be checked out without a complete repository mapping", request.WorkspaceName))
		}
		for _, checkout := range stored.Checkouts {
			if checkout.Detached || checkout.Branch != request.WorkspaceName {
				return WorkspacePlanRequest{}, NewError(ErrorValidation, fmt.Errorf("workspace %q has incompatible stored checkout %q; checkout requires branch %q", request.WorkspaceName, checkout.RepositoryID, request.WorkspaceName))
			}
		}
		if target == "" {
			target = stored.RootPath
		}
		mounts = overlayStoredMounts(stored, mounts)
	}
	return WorkspacePlanRequest{
		Operation: plan.Checkout, WorkspaceName: request.WorkspaceName, TargetPath: target,
		WorktreeRoot: request.WorktreeRoot, DataDir: request.DataDir, Mounts: mounts,
	}, nil
}

func overlayStoredMounts(stored domain.Workspace, overrides []MountOverride) []MountOverride {
	values := make(map[string]string, len(stored.Checkouts)+len(overrides))
	for _, checkout := range stored.Checkouts {
		values[checkout.RepositoryID] = checkout.Mount
	}
	for _, override := range overrides {
		values[override.RepositoryID] = override.Mount
	}
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	merged := make([]MountOverride, 0, len(ids))
	for _, id := range ids {
		merged = append(merged, MountOverride{RepositoryID: id, Mount: values[id]})
	}
	return merged
}

// ListWorkspaces loads complete, validated state files in deterministic order.
func ListWorkspaces(project domain.Project, dataDir string) ([]domain.Workspace, error) {
	entries, err := os.ReadDir(WorkspaceStateDirectory(dataDir, project.ID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, NewError(ErrorInternal, fmt.Errorf("read workspace state: %w", err))
	}
	workspaces := make([]domain.Workspace, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		state, err := store.ReadWorkspace(filepath.Join(WorkspaceStateDirectory(dataDir, project.ID), entry.Name()))
		if err != nil {
			return nil, NewError(ErrorValidation, fmt.Errorf("read workspace state %q: %w", entry.Name(), err))
		}
		workspace, err := workspaceFromState(state)
		if err != nil {
			return nil, NewError(ErrorValidation, fmt.Errorf("decode workspace state %q: %w", entry.Name(), err))
		}
		if err := workspace.Validate(project); err != nil {
			return nil, NewError(ErrorValidation, fmt.Errorf("validate workspace state %q: %w", entry.Name(), err))
		}
		workspaces = append(workspaces, workspace)
	}
	sort.Slice(workspaces, func(left, right int) bool {
		if workspaces[left].Name == "default" {
			return workspaces[right].Name != "default"
		}
		if workspaces[right].Name == "default" {
			return false
		}
		return workspaces[left].Name < workspaces[right].Name
	})
	return workspaces, nil
}

// FindWorkspace resolves a logical workspace name (or persisted ID) from the
// single authoritative state directory.
func FindWorkspace(project domain.Project, dataDir, name string) (domain.Workspace, bool, error) {
	workspaces, err := ListWorkspaces(project, dataDir)
	if err != nil {
		return domain.Workspace{}, false, err
	}
	var matches []domain.Workspace
	for _, workspace := range workspaces {
		if workspace.Name == name || workspace.ID == name {
			matches = append(matches, workspace)
		}
	}
	if len(matches) == 0 {
		return domain.Workspace{}, false, nil
	}
	if len(matches) > 1 {
		return domain.Workspace{}, false, NewError(ErrorConflict, fmt.Errorf("workspace selector %q is ambiguous", name))
	}
	return matches[0], true, nil
}

// RequireWorkspace turns an unknown workspace into the stable lookup error.
func RequireWorkspace(project domain.Project, dataDir, name string) (domain.Workspace, error) {
	workspace, found, err := FindWorkspace(project, dataDir, name)
	if err != nil {
		return domain.Workspace{}, err
	}
	if !found {
		return domain.Workspace{}, NewError(ErrorWorkspaceNotFound, fmt.Errorf("workspace %q was not found", name))
	}
	return workspace, nil
}
