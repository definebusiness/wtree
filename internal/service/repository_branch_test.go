package service_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/definebusiness/wtree/internal/config"
	"github.com/definebusiness/wtree/internal/domain"
	"github.com/definebusiness/wtree/internal/fsutil"
	gitadapter "github.com/definebusiness/wtree/internal/git"
	"github.com/definebusiness/wtree/internal/lock"
	"github.com/definebusiness/wtree/internal/plan"
	"github.com/definebusiness/wtree/internal/service"
	"github.com/definebusiness/wtree/internal/store"
)

// RED: prior to M02 there was no operation which could change the configured
// companion baseline without changing a checkout. GREEN proves the one
// configuration-only publication and the plan consumed by a later create.
func TestRepositoryBranchPlansPublishesAndLeavesExistingBranchesUntouched(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	beforeLocal := mustRepositoryBranchRead(t, project.ConfigPath)
	local, err := config.LoadProject(beforeLocal)
	if err != nil {
		t.Fatal(err)
	}
	portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
	beforePortable := mustRepositoryBranchRead(t, portablePath)
	portableBeforeValue, err := config.LoadPortableManifest(beforePortable)
	if err != nil {
		t.Fatal(err)
	}
	serviceValue := service.NewRepositoryBranchService()
	dry, err := serviceValue.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next", DryRun: true})
	if err != nil || dry.Status != "planned" || !dry.PortableChanged || !dry.LocalChanged {
		t.Fatalf("dry result = %#v, %v", dry, err)
	}
	if got := mustRepositoryBranchRead(t, project.ConfigPath); !bytes.Equal(got, beforeLocal) {
		t.Fatal("dry-run rewrote local configuration")
	}
	if got := mustRepositoryBranchRead(t, portablePath); !bytes.Equal(got, beforePortable) {
		t.Fatal("dry-run rewrote portable manifest")
	}
	result, err := serviceValue.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
	if err != nil || result.Status != "completed" || result.PreviousBaseline != "main" || result.Baseline != "next" {
		t.Fatalf("publish result = %#v, %v", result, err)
	}
	updatedLocal, err := config.LoadProject(mustRepositoryBranchRead(t, project.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	updatedPortable, err := config.LoadPortableManifest(mustRepositoryBranchRead(t, portablePath))
	if err != nil {
		t.Fatal(err)
	}
	if updatedLocal.Repositories["backend"].DefaultBranch != "next" || updatedPortable.Repositories["backend"].DefaultBranch != "next" || updatedPortable.Repositories["backend"].Upstream.Branch != "next" {
		t.Fatalf("published configurations are not aligned: %#v %#v", updatedLocal.Repositories["backend"], updatedPortable.Repositories["backend"])
	}
	wantLocal := local
	localRepository := wantLocal.Repositories["backend"]
	localRepository.DefaultBranch = "next"
	wantLocal.Repositories["backend"] = localRepository
	wantPortable := portableBeforeValue
	portableRepository := wantPortable.Repositories["backend"]
	portableRepository.DefaultBranch, portableRepository.Upstream.Branch = "next", "next"
	wantPortable.Repositories["backend"] = portableRepository
	if !reflect.DeepEqual(updatedLocal, wantLocal) || !reflect.DeepEqual(updatedPortable, wantPortable) {
		t.Fatalf("baseline publication changed unrelated authority: local=%#v portable=%#v", updatedLocal, updatedPortable)
	}
	project = reloadCompanionFixtureProject(t, project, data)
	localInfo, err := os.Stat(project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	portableInfo, err := os.Stat(portablePath)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := serviceValue.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
	if err != nil || unchanged.Status != "unchanged" || unchanged.PortableChanged || unchanged.LocalChanged {
		t.Fatalf("no-op = %#v, %v", unchanged, err)
	}
	if info, statErr := os.Stat(project.ConfigPath); statErr != nil || !info.ModTime().Equal(localInfo.ModTime()) {
		t.Fatalf("no-op rewrote local timestamp: %v", statErr)
	}
	if info, statErr := os.Stat(portablePath); statErr != nil || !info.ModTime().Equal(portableInfo.ModTime()) {
		t.Fatalf("no-op rewrote portable timestamp: %v", statErr)
	}
	planned, err := service.NewWorkspacePlanner().Plan(context.Background(), project, service.WorkspacePlanRequest{Operation: plan.Create, WorkspaceName: "future-baseline", TargetPath: filepath.Join(t.TempDir(), "future"), DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range planned.Repositories {
		if repository.ID == "backend" && repository.Baseline != "next" {
			t.Fatalf("future create baseline = %#v", repository)
		}
	}
}

func TestRepositoryBranchRejectsInvalidAuthorityWithoutWriting(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	local, err := config.ReadProjectFile(project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
	beforeLocal, beforePortable := mustRepositoryBranchRead(t, project.ConfigPath), mustRepositoryBranchRead(t, portablePath)
	for _, request := range []service.RepositoryBranchRequest{
		{Project: project, DataDir: data, RepositoryID: "missing", Branch: "next"},
		{Project: project, DataDir: data, RepositoryID: "backend", Branch: "does-not-exist"},
		{Project: project, DataDir: data, RepositoryID: "backend", Branch: "bad..branch"},
	} {
		if _, err := service.NewRepositoryBranchService().Execute(context.Background(), request); err == nil {
			t.Fatalf("request %#v succeeded", request)
		}
		if got := mustRepositoryBranchRead(t, project.ConfigPath); !bytes.Equal(got, beforeLocal) {
			t.Fatal("rejected request rewrote local configuration")
		}
		if got := mustRepositoryBranchRead(t, portablePath); !bytes.Equal(got, beforePortable) {
			t.Fatal("rejected request rewrote portable manifest")
		}
	}
}

func TestRepositoryBranchRejectsOrdinaryRepositoryWithoutWriting(t *testing.T) {
	project, _, _, data := createFixture(t)
	before := mustRepositoryBranchRead(t, project.ConfigPath)
	if _, err := service.NewRepositoryBranchService().Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "root", Branch: "main"}); err == nil {
		t.Fatal("ordinary repository was accepted")
	}
	if after := mustRepositoryBranchRead(t, project.ConfigPath); !bytes.Equal(after, before) {
		t.Fatal("ordinary rejection rewrote configuration")
	}
}

func TestRepositoryBranchRejectsV3AuthorityIdentityMismatchAndCancellationWithoutWriting(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	before := mustRepositoryBranchRead(t, project.ConfigPath)
	local, err := config.LoadProject(before)
	if err != nil {
		t.Fatal(err)
	}
	local.Version = config.ProjectConfigVersion3
	for id, repository := range local.Repositories {
		repository.Companion = false
		local.Repositories[id] = repository
	}
	v3, err := config.MarshalProject(local)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project.ConfigPath, v3, 0o600); err != nil {
		t.Fatal(err)
	}
	request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
	if _, err := service.NewRepositoryBranchService().Plan(context.Background(), request); err == nil {
		t.Fatal("v3 local authority was accepted")
	}
	if err := os.WriteFile(project.ConfigPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	wrongIdentity := project
	for index := range wrongIdentity.Repositories {
		if wrongIdentity.Repositories[index].ID == "backend" {
			wrongIdentity.Repositories[index].CommonGitDir = "/wrong-identity"
		}
	}
	request.Project = wrongIdentity
	if _, err := service.NewRepositoryBranchService().Plan(context.Background(), request); err == nil {
		t.Fatal("wrong CommonGitDir was accepted")
	}
	request.Project = project
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.NewRepositoryBranchService().Plan(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled plan = %v", err)
	}
	if got := mustRepositoryBranchRead(t, project.ConfigPath); !bytes.Equal(got, before) {
		t.Fatal("validation changed local configuration")
	}
}

type repositoryBranchFaultGit struct {
	gitadapter.Git
	stage string
}

func (g repositoryBranchFaultGit) ValidBranchName(ctx context.Context, path, branch string) (bool, error) {
	if g.stage == "valid" {
		return false, errors.New("injected valid-name observation")
	}
	return g.Git.ValidBranchName(ctx, path, branch)
}
func (g repositoryBranchFaultGit) CommonGitDir(ctx context.Context, path string) (string, error) {
	if g.stage == "common" {
		return "", errors.New("injected identity observation")
	}
	return g.Git.CommonGitDir(ctx, path)
}
func (g repositoryBranchFaultGit) BranchExists(ctx context.Context, path, branch string) (bool, error) {
	if g.stage == "exists" {
		return false, errors.New("injected branch observation")
	}
	return g.Git.BranchExists(ctx, path, branch)
}
func (g repositoryBranchFaultGit) ResolveRef(ctx context.Context, path, ref string) (string, error) {
	if g.stage == "resolve" {
		return "", errors.New("injected branch-tip observation")
	}
	return g.Git.ResolveRef(ctx, path, ref)
}

func TestRepositoryBranchObservationAndRecoveryGuardsAreTypedAndNonMutating(t *testing.T) {
	for _, stage := range []string{"valid", "common", "exists", "resolve"} {
		t.Run(stage, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			backend.Run(t, "branch", "next")
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			before := mustRepositoryBranchRead(t, project.ConfigPath)
			value := service.NewRepositoryBranchServiceWith(repositoryBranchFaultGit{Git: gitadapter.NewAdapter("git"), stage: stage}, lock.Manager{}, os.ReadFile, os.Stat, nil, store.WriteRecoveryCAS)
			_, err := value.Plan(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
			var application *service.Error
			if err == nil || !errors.As(err, &application) || application.Kind != service.ErrorGit {
				t.Fatalf("%s error = %v", stage, err)
			}
			if got := mustRepositoryBranchRead(t, project.ConfigPath); !bytes.Equal(got, before) {
				t.Fatal("observation failure mutated local config")
			}
		})
	}
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	recovery := filepath.Join(data, "projects", project.ID, "recovery", "existing.json")
	if err := store.WriteRecovery(recovery, store.RecoveryRecord{ProjectID: project.ID, WorkspaceID: "existing", Operation: "repo-branch", FailedStep: "publish"}); err != nil {
		t.Fatal(err)
	}
	_, err := service.NewRepositoryBranchService().Plan(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
	var application *service.Error
	if err == nil || !errors.As(err, &application) || application.Kind != service.ErrorConflict {
		t.Fatalf("recovery guard = %v", err)
	}
	project, root, backend, data = createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	recoveryDirectory := filepath.Join(data, "projects", project.ID, "recovery")
	if err := os.MkdirAll(filepath.Dir(recoveryDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recoveryDirectory, []byte("not a recovery directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = service.NewRepositoryBranchService().Plan(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
	if err == nil || !errors.As(err, &application) || application.Kind != service.ErrorConflict {
		t.Fatalf("recovery inspection error = %v", err)
	}
}

func TestRepositoryBranchRejectsVersionAndSplitAuthorityMatrix(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.ProjectConfig, *config.PortableManifest, *domain.Project)
	}{
		{"local-v3", func(local *config.ProjectConfig, _ *config.PortableManifest, _ *domain.Project) {
			local.Version = config.ProjectConfigVersion3
			for id, repository := range local.Repositories {
				repository.Companion = false
				local.Repositories[id] = repository
			}
		}},
		{"portable-v3", func(_ *config.ProjectConfig, portable *config.PortableManifest, _ *domain.Project) {
			portable.Version = config.PortableManifestVersion3
			for id, repository := range portable.Repositories {
				repository.Companion = false
				portable.Repositories[id] = repository
			}
		}},
		{"selected-local-default", func(local *config.ProjectConfig, _ *config.PortableManifest, _ *domain.Project) {
			repository := local.Repositories["backend"]
			repository.DefaultBranch = "next"
			local.Repositories["backend"] = repository
		}},
		{"local-source-path", func(local *config.ProjectConfig, _ *config.PortableManifest, _ *domain.Project) {
			repository := local.Repositories["backend"]
			repository.Source = "."
			local.Repositories["backend"] = repository
		}},
		{"project-metadata", func(_ *config.ProjectConfig, _ *config.PortableManifest, project *domain.Project) {
			project.Name = "Other"
		}},
		{"unrelated-portable-default", func(_ *config.ProjectConfig, portable *config.PortableManifest, _ *domain.Project) {
			repository := portable.Repositories["root"]
			repository.DefaultBranch, repository.Upstream.Branch = "next", "next"
			portable.Repositories["root"] = repository
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			backend.Run(t, "branch", "next")
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			local, err := config.ReadProjectFile(project.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			manifestPath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
			portable, err := config.LoadPortableManifest(mustRepositoryBranchRead(t, manifestPath))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&local, &portable, &project)
			localBytes, err := config.MarshalProject(local)
			if err != nil {
				t.Fatal(err)
			}
			portableBytes, err := config.MarshalPortableManifest(portable)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(project.ConfigPath, localBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, portableBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := service.NewRepositoryBranchService().Plan(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}); err == nil {
				t.Fatal("split authority was accepted")
			}
		})
	}
}

func TestRepositoryBranchRejectsStalePlanAndRollsBackPartialPublication(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
	beforeLocal := mustRepositoryBranchRead(t, project.ConfigPath)
	local, err := config.LoadProject(beforeLocal)
	if err != nil {
		t.Fatal(err)
	}
	portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
	beforePortable := mustRepositoryBranchRead(t, portablePath)
	planned, err := service.NewRepositoryBranchService().Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project.ConfigPath, append(beforeLocal, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewRepositoryBranchService().ExecutePlan(context.Background(), request, planned); err == nil {
		t.Fatal("stale plan succeeded")
	}
	if err := os.WriteFile(project.ConfigPath, beforeLocal, 0o600); err != nil {
		t.Fatal(err)
	}

	writes := 0
	writer := func(path string, value []byte, mode os.FileMode, _ os.FileInfo, compare func() error) error {
		writes++
		if writes == 2 {
			return errors.New("injected local publication failure")
		}
		if compare != nil {
			if err := compare(); err != nil {
				return err
			}
		}
		return fsutil.WriteFileAtomicMode(path, value, mode)
	}
	value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Stat, writer, store.WriteRecoveryCAS)
	if _, err := value.Execute(context.Background(), request); err == nil || !service.HasCleanRollback(err) {
		t.Fatalf("partial publication = %v, want clean rollback", err)
	}
	if got := mustRepositoryBranchRead(t, project.ConfigPath); !bytes.Equal(got, beforeLocal) {
		t.Fatal("local generation was not restored")
	}
	if got := mustRepositoryBranchRead(t, portablePath); !bytes.Equal(got, beforePortable) {
		t.Fatal("portable generation was not restored")
	}
}

func TestRepositoryBranchRecordsOwnedRecoveryWhenRollbackCannotRestore(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
	writes := 0
	var recoveryPath string
	writer := func(path string, value []byte, mode os.FileMode, _ os.FileInfo, compare func() error) error {
		writes++
		if writes == 2 || writes == 3 {
			return errors.New("injected publication/rollback failure")
		}
		if compare != nil {
			if err := compare(); err != nil {
				return err
			}
		}
		return fsutil.WriteFileAtomicMode(path, value, mode)
	}
	recoverWriter := func(path string, record store.RecoveryRecord, compare func() error) error {
		recoveryPath = path
		return store.WriteRecoveryCAS(path, record, compare)
	}
	value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Stat, writer, recoverWriter)
	if _, err := value.Execute(context.Background(), request); err == nil {
		t.Fatal("incomplete rollback succeeded")
	} else {
		var application *service.Error
		if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
			t.Fatalf("incomplete rollback error = %v", err)
		}
	}
	record, err := store.ReadRecovery(recoveryPath)
	if err != nil || record.Operation != "repo-branch" || record.FailedStep != "publish-local" || len(record.UnrevertedSteps) != 1 || record.UnrevertedSteps[0] != "portable" {
		t.Fatalf("recovery = %#v, %v", record, err)
	}
	if _, err := value.Plan(context.Background(), request); err == nil {
		t.Fatal("recovery conflict did not block retry")
	}
}

func TestRepositoryBranchRejectsTamperedAndStaleUnchangedPlansWithoutWriting(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
	implementation := service.NewRepositoryBranchService()
	if _, err := implementation.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	project = reloadCompanionFixtureProject(t, project, data)
	request.Project = project
	plan, err := implementation.Plan(context.Background(), request)
	if err != nil || plan.Changed() {
		t.Fatalf("unchanged plan = %#v, %v", plan, err)
	}
	tampered := plan
	tampered.Baseline = "other"
	if _, err := implementation.ExecutePlan(context.Background(), request, tampered); err == nil {
		t.Fatal("tampered public plan claim executed")
	}
	before := mustRepositoryBranchRead(t, project.ConfigPath)
	if err := os.WriteFile(project.ConfigPath, append(before, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.ExecutePlan(context.Background(), request, plan); err == nil {
		t.Fatal("stale unchanged plan executed")
	}
	if err := os.WriteFile(project.ConfigPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryBranchPlanBindsCanonicalDataDirectoryBeforeLockOrRecovery(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
	implementation := service.NewRepositoryBranchService()
	planned, err := implementation.Plan(context.Background(), request)
	if err != nil || !filepath.IsAbs(planned.DataDir) {
		t.Fatalf("plan data authority = %#v, %v", planned, err)
	}
	before := mustRepositoryBranchRead(t, project.ConfigPath)
	alternate := t.TempDir()
	locker := &repositoryBranchUnexpectedLocker{}
	bound := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), locker, os.ReadFile, os.Stat, nil, store.WriteRecoveryCAS)
	wrong := request
	wrong.DataDir = alternate
	if _, err := bound.ExecutePlan(context.Background(), wrong, planned); err == nil {
		t.Fatal("alternate data directory was accepted")
	}
	tampered := planned
	tampered.DataDir = alternate
	if _, err := bound.ExecutePlan(context.Background(), request, tampered); err == nil {
		t.Fatal("tampered data directory plan was accepted")
	}
	if locker.calls != 0 || !bytes.Equal(mustRepositoryBranchRead(t, project.ConfigPath), before) {
		t.Fatalf("wrong data authority acquired lock or wrote config: calls=%d", locker.calls)
	}
	if _, err := os.Stat(filepath.Join(alternate, "projects", project.ID, "recovery")); !os.IsNotExist(err) {
		t.Fatalf("alternate data directory received recovery mutation: %v", err)
	}
}

type repositoryBranchUnexpectedLocker struct{ calls int }

func (locker *repositoryBranchUnexpectedLocker) ProjectLock(context.Context, string, string, time.Duration) (lock.Handle, error) {
	locker.calls++
	return nil, errors.New("unexpected lock acquisition")
}

func TestRepositoryBranchPlanBindsSelectedBranchTip(t *testing.T) {
	for _, mutation := range []string{"advance", "delete-recreate"} {
		t.Run(mutation, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			backend.Run(t, "branch", "next")
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
			implementation := service.NewRepositoryBranchService()
			planned, err := implementation.Plan(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			before := mustRepositoryBranchRead(t, project.ConfigPath)
			backend.CommitFile("tip-"+mutation+".txt", mutation+"\n", mutation)
			if mutation == "delete-recreate" {
				backend.Run(t, "branch", "-D", "next")
			}
			backend.Run(t, "branch", "-f", "next", "HEAD")
			if _, err := implementation.ExecutePlan(context.Background(), request, planned); err == nil {
				t.Fatal("moved branch tip was accepted")
			} else {
				var application *service.Error
				if !errors.As(err, &application) || application.Kind != service.ErrorConflict {
					t.Fatalf("moved branch tip error = %v", err)
				}
			}
			if !bytes.Equal(mustRepositoryBranchRead(t, project.ConfigPath), before) {
				t.Fatal("moved branch tip changed configuration")
			}
		})
	}
}

func TestRepositoryBranchRevalidateBranchTipObservationFailureAndCancellationDoNotPublish(t *testing.T) {
	for _, failure := range []error{errors.New("injected revalidation resolve failure"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			backend.Run(t, "branch", "next")
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
			git := &repositoryBranchRevalidateResolveGit{Git: gitadapter.NewAdapter("git"), second: failure}
			implementation := service.NewRepositoryBranchServiceWith(git, lock.Manager{}, os.ReadFile, os.Stat, nil, store.WriteRecoveryCAS)
			planned, err := implementation.Plan(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			before := mustRepositoryBranchRead(t, project.ConfigPath)
			_, err = implementation.ExecutePlan(context.Background(), request, planned)
			if failure == context.Canceled {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("resolve cancellation = %v", err)
				}
			} else {
				var application *service.Error
				if !errors.As(err, &application) || application.Kind != service.ErrorGit {
					t.Fatalf("resolve observation error = %v", err)
				}
			}
			if !bytes.Equal(mustRepositoryBranchRead(t, project.ConfigPath), before) {
				t.Fatal("resolve revalidation failure changed configuration")
			}
		})
	}
}

func TestRepositoryBranchRejectsSymlinkAndNonRegularConfigurationTargets(t *testing.T) {
	for _, target := range []struct {
		name      string
		portable  bool
		directory bool
	}{
		{name: "local-symlink"},
		{name: "portable-symlink", portable: true},
		{name: "portable-directory", portable: true, directory: true},
	} {
		t.Run(target.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			backend.Run(t, "branch", "next")
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			local, err := config.ReadProjectFile(project.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			path := project.ConfigPath
			if target.portable {
				path = filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
			}
			before := mustRepositoryBranchRead(t, path)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if target.directory {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				backing := filepath.Join(t.TempDir(), "backing")
				if err := os.WriteFile(backing, before, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(backing, path); err != nil {
					t.Fatal(err)
				}
			}
			_, err = service.NewRepositoryBranchService().Plan(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
			var application *service.Error
			if !errors.As(err, &application) || application.Kind != service.ErrorConflict {
				t.Fatalf("unsafe target error = %v", err)
			}
			info, statErr := os.Lstat(path)
			if statErr != nil || (target.directory && !info.IsDir()) || (!target.directory && info.Mode()&os.ModeSymlink == 0) {
				t.Fatalf("unsafe target was altered: info=%v err=%v", info, statErr)
			}
			if !target.directory {
				if got := mustRepositoryBranchRead(t, path); !bytes.Equal(got, before) {
					t.Fatal("symlink target bytes changed")
				}
			}
		})
	}
}

type repositoryBranchRevalidateResolveGit struct {
	gitadapter.Git
	calls  int
	second error
}

func (g *repositoryBranchRevalidateResolveGit) ResolveRef(ctx context.Context, path, ref string) (string, error) {
	g.calls++
	if g.calls > 1 {
		return "", g.second
	}
	return g.Git.ResolveRef(ctx, path, ref)
}

func TestRepositoryBranchRecoveryListsOnlyTheResidualGeneration(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	local, err := config.ReadProjectFile(project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
	writes := 0
	writer := func(path string, value []byte, mode os.FileMode, _ os.FileInfo, compare func() error) error {
		if compare != nil {
			if err := compare(); err != nil {
				return err
			}
		}
		writes++
		if err := fsutil.WriteFileAtomicMode(path, value, mode); err != nil {
			return err
		}
		if writes == 2 {
			return os.WriteFile(portablePath, []byte("foreign portable generation\n"), 0o600)
		}
		return nil
	}
	value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Stat, writer, store.WriteRecoveryCAS)
	request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
	if _, err := value.Execute(context.Background(), request); err == nil {
		t.Fatal("foreign post-publication replacement succeeded")
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", "repo-branch-backend.json")
	record, err := store.ReadRecovery(recoveryPath)
	if err != nil || !bytes.Equal([]byte(strings.Join(record.CompletedSteps, ",")), []byte("portable,local")) || !reflect.DeepEqual(record.UnrevertedSteps, []string{"portable"}) {
		t.Fatalf("residual recovery = %#v, %v", record, err)
	}
}

// A replacement after the first publish is not ours to restore.  The second
// file has not been published yet, so the recovery record must name precisely
// the one residual generation instead of claiming a split it did not create.
func TestRepositoryBranchPostFirstForeignReplacementRecordsOnlyPortableResidual(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	local, err := config.ReadProjectFile(project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
	beforeLocal := mustRepositoryBranchRead(t, project.ConfigPath)
	writes := 0
	writer := func(path string, value []byte, mode os.FileMode, _ os.FileInfo, compare func() error) error {
		if err := compare(); err != nil {
			return err
		}
		writes++
		if err := fsutil.WriteFileAtomicMode(path, value, mode); err != nil {
			return err
		}
		if writes == 1 {
			return os.WriteFile(portablePath, []byte("foreign portable generation\n"), 0o600)
		}
		return nil
	}
	value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Stat, writer, store.WriteRecoveryCAS)
	_, err = value.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("post-first replacement error = %v", err)
	}
	if got := mustRepositoryBranchRead(t, project.ConfigPath); !bytes.Equal(got, beforeLocal) {
		t.Fatal("post-first replacement changed local generation")
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", "repo-branch-backend.json")
	record, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || !reflect.DeepEqual(record.CompletedSteps, []string{"portable"}) || !reflect.DeepEqual(record.UnrevertedSteps, []string{"portable"}) {
		t.Fatalf("post-first recovery = %#v, %v", record, readErr)
	}
}

func TestRepositoryBranchRollbackReverseStagesAndRecoveryPublicationFailures(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		recover func(string, store.RecoveryRecord, func() error) error
	}{
		{name: "writer", recover: func(string, store.RecoveryRecord, func() error) error {
			return errors.New("injected recovery writer failure")
		}},
		{name: "cas-conflict", recover: func(path string, _ store.RecoveryRecord, compare func() error) error {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte("foreign recovery generation\n"), 0o600); err != nil {
				return err
			}
			return compare()
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			backend.Run(t, "branch", "next")
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			local, err := config.ReadProjectFile(project.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
			beforePortable := mustRepositoryBranchRead(t, portablePath)
			lstatBeforeVerificationFailure := 0
			stat := func(path string) (os.FileInfo, error) {
				if lstatBeforeVerificationFailure > 0 {
					lstatBeforeVerificationFailure--
					if lstatBeforeVerificationFailure == 0 {
						return nil, errors.New("injected post-publication verification failure")
					}
				}
				return os.Stat(path)
			}
			writes := 0
			var order []string
			writer := func(path string, value []byte, mode os.FileMode, _ os.FileInfo, compare func() error) error {
				order = append(order, filepath.Base(path))
				writes++
				if writes == 3 { // reverse-stage local restore fails; portable still restores.
					return errors.New("injected local rollback failure")
				}
				if err := compare(); err != nil {
					return err
				}
				if err := fsutil.WriteFileAtomicMode(path, value, mode); err != nil {
					return err
				}
				if writes == 2 { // fail verification after both exact generations publish.
					// First observation records the installed local identity; the
					// next one is the post-publication verification boundary.
					lstatBeforeVerificationFailure = 2
				}
				return nil
			}
			value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, stat, writer, scenario.recover)
			_, err = value.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
			var application *service.Error
			if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
				t.Fatalf("recovery publication error = %v", err)
			}
			if !bytes.Equal(mustRepositoryBranchRead(t, portablePath), beforePortable) {
				t.Fatal("portable was not restored after the later reverse stage")
			}
			if len(order) != 4 {
				t.Fatalf("publication/rollback calls = %v, want both reverse stages", order)
			}
			recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", "repo-branch-backend.json")
			if scenario.name == "cas-conflict" {
				if got := mustRepositoryBranchRead(t, recoveryPath); !bytes.Equal(got, []byte("foreign recovery generation\n")) {
					t.Fatalf("recovery CAS overwrote foreign generation: %q", got)
				}
			}
			if _, retryErr := value.Plan(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}); retryErr == nil {
				t.Fatal("retry was not blocked until the residual local generation is reconciled")
			}
		})
	}
}

func TestRepositoryBranchFirstWriteFailureLeavesBothGenerationsUntouched(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	local, err := config.ReadProjectFile(project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
	beforeLocal, beforePortable := mustRepositoryBranchRead(t, project.ConfigPath), mustRepositoryBranchRead(t, portablePath)
	writer := func(string, []byte, os.FileMode, os.FileInfo, func() error) error {
		return errors.New("injected first portable write failure")
	}
	value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Stat, writer, store.WriteRecoveryCAS)
	_, err = value.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
	if err == nil || !service.HasCleanRollback(err) {
		t.Fatalf("first write error = %v", err)
	}
	if got := mustRepositoryBranchRead(t, project.ConfigPath); !bytes.Equal(got, beforeLocal) {
		t.Fatal("first write failure changed local")
	}
	if got := mustRepositoryBranchRead(t, portablePath); !bytes.Equal(got, beforePortable) {
		t.Fatal("first write failure changed portable")
	}
}

func TestRepositoryBranchPostReplacementPublicationFailuresRollBackInstalledGeneration(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("dir-sync-%d", failAt), func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			backend.Run(t, "branch", "next")
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			local, err := config.ReadProjectFile(project.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
			beforeLocal, beforePortable := mustRepositoryBranchRead(t, project.ConfigPath), mustRepositoryBranchRead(t, portablePath)
			writes := 0
			writer := func(path string, value []byte, mode os.FileMode, _ os.FileInfo, compare func() error) error {
				writes++
				if err := compare(); err != nil {
					return err
				}
				return fsutil.WriteFileAtomicModeWithHook(path, value, mode, func(step string) error {
					if writes == failAt && step == "dir-sync" {
						return errors.New("injected post-replacement directory sync failure")
					}
					return nil
				})
			}
			value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Lstat, writer, store.WriteRecoveryCAS)
			_, err = value.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
			if err == nil || !service.HasCleanRollback(err) {
				t.Fatalf("post-replacement failure = %v", err)
			}
			if !bytes.Equal(mustRepositoryBranchRead(t, project.ConfigPath), beforeLocal) || !bytes.Equal(mustRepositoryBranchRead(t, portablePath), beforePortable) {
				t.Fatal("post-replacement error left split configuration")
			}
		})
	}
}

func TestRepositoryBranchNilWriterSuccessWithoutPostWriteReceiptRecordsCurrentOwnership(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	local, err := config.ReadProjectFile(project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
	failReceipt := false
	lstat := func(path string) (os.FileInfo, error) {
		if failReceipt {
			failReceipt = false
			return nil, errors.New("injected post-write receipt failure")
		}
		return os.Lstat(path)
	}
	writer := func(path string, value []byte, mode os.FileMode, expected os.FileInfo, compare func() error) error {
		if err := compare(); err != nil {
			return err
		}
		if err := fsutil.WriteFileAtomicModeExpected(path, value, mode, expected); err != nil {
			return err
		}
		failReceipt = true
		return nil
	}
	value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, lstat, writer, store.WriteRecoveryCAS)
	request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
	_, err = value.Execute(context.Background(), request)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("missing receipt error = %v", err)
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", "repo-branch-backend.json")
	record, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || !reflect.DeepEqual(record.CompletedSteps, []string{"portable"}) || !reflect.DeepEqual(record.UnrevertedSteps, []string{"portable"}) || len(record.RollbackFailures) != 1 || record.RollbackFailures[0].Step != "observe-portable" {
		t.Fatalf("missing receipt recovery = %#v, %v", record, readErr)
	}
	if _, retryErr := value.Plan(context.Background(), request); retryErr == nil {
		t.Fatal("missing receipt recovery did not block retry")
	}
	if got := mustRepositoryBranchRead(t, project.ConfigPath); bytes.Contains(got, []byte("default_branch: next")) {
		t.Fatal("missing receipt path published local configuration")
	}
	if got := mustRepositoryBranchRead(t, portablePath); !bytes.Contains(got, []byte("default_branch: next")) {
		t.Fatal("missing receipt path lost the acknowledged portable generation")
	}
}

func TestRepositoryBranchSecondNilWriterSuccessWithoutReceiptRestoresPriorGeneration(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	local, err := config.ReadProjectFile(project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
	beforePortable := mustRepositoryBranchRead(t, portablePath)
	failReceipt := false
	lstat := func(path string) (os.FileInfo, error) {
		if failReceipt {
			failReceipt = false
			return nil, errors.New("injected second post-write receipt failure")
		}
		return os.Lstat(path)
	}
	writes := 0
	writer := func(path string, value []byte, mode os.FileMode, expected os.FileInfo, compare func() error) error {
		if err := compare(); err != nil {
			return err
		}
		if err := fsutil.WriteFileAtomicModeExpected(path, value, mode, expected); err != nil {
			return err
		}
		writes++
		if writes == 2 {
			failReceipt = true
		}
		return nil
	}
	value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, lstat, writer, store.WriteRecoveryCAS)
	request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
	_, err = value.Execute(context.Background(), request)
	var application *service.Error
	if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
		t.Fatalf("second missing receipt error = %v", err)
	}
	recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", "repo-branch-backend.json")
	record, readErr := store.ReadRecovery(recoveryPath)
	if readErr != nil || !reflect.DeepEqual(record.CompletedSteps, []string{"portable", "local"}) || !reflect.DeepEqual(record.UnrevertedSteps, []string{"local"}) || len(record.RollbackFailures) != 1 || record.RollbackFailures[0].Step != "observe-local" {
		t.Fatalf("second missing receipt recovery = %#v, %v", record, readErr)
	}
	if got := mustRepositoryBranchRead(t, portablePath); !bytes.Equal(got, beforePortable) {
		t.Fatal("prior portable generation was not safely restored")
	}
	if got := mustRepositoryBranchRead(t, project.ConfigPath); !bytes.Contains(got, []byte("default_branch: next")) {
		t.Fatal("uncertain local generation disappeared")
	}
	if _, retryErr := value.Plan(context.Background(), request); retryErr == nil {
		t.Fatal("second missing receipt recovery did not block retry")
	}
}

func TestRepositoryBranchAuxiliaryAtomicOutcomeRecordsEveryPublicationBoundary(t *testing.T) {
	for _, source := range []string{"conditional-restore", "displaced-expected-cleanup"} {
		for _, failAt := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s-publish-%d", source, failAt), func(t *testing.T) {
				project, root, backend, data := createFixture(t)
				backend.Run(t, "branch", "next")
				publishCompanionBaseline(t, project, root, "backend", "main", false)
				project = reloadCompanionFixtureProject(t, project, data)
				local, err := config.ReadProjectFile(project.ConfigPath)
				if err != nil {
					t.Fatal(err)
				}
				portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
				beforeLocal, beforePortable := mustRepositoryBranchRead(t, project.ConfigPath), mustRepositoryBranchRead(t, portablePath)
				writes := 0
				var auxiliary string
				writer := func(path string, value []byte, mode os.FileMode, expected os.FileInfo, compare func() error) error {
					if err := compare(); err != nil {
						return err
					}
					if err := fsutil.WriteFileAtomicModeExpected(path, value, mode, expected); err != nil {
						return err
					}
					writes++
					if writes == failAt {
						auxiliary = path + ".retained-foreign"
						if err := os.WriteFile(auxiliary, []byte("foreign retained generation\n"), 0o600); err != nil {
							return err
						}
						return &fsutil.AuxiliaryOutcomeError{Paths: []string{auxiliary}, Err: fmt.Errorf("injected %s auxiliary generation", source)}
					}
					return nil
				}
				value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Lstat, writer, store.WriteRecoveryCAS)
				request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
				_, err = value.Execute(context.Background(), request)
				var application *service.Error
				if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
					t.Fatalf("auxiliary error = %v", err)
				}
				if got := mustRepositoryBranchRead(t, auxiliary); !bytes.Equal(got, []byte("foreign retained generation\n")) {
					t.Fatalf("auxiliary generation lost: %q", got)
				}
				if !bytes.Equal(mustRepositoryBranchRead(t, project.ConfigPath), beforeLocal) || !bytes.Equal(mustRepositoryBranchRead(t, portablePath), beforePortable) {
					t.Fatal("auxiliary outcome claimed clean destination rollback")
				}
				recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", "repo-branch-backend.json")
				record, readErr := store.ReadRecovery(recoveryPath)
				wantCompleted := []string{"portable"}
				if failAt == 2 {
					wantCompleted = []string{"portable", "local"}
				}
				wantAux := "auxiliary:" + auxiliary
				if readErr != nil || !reflect.DeepEqual(record.CompletedSteps, wantCompleted) || !reflect.DeepEqual(record.UnrevertedSteps, []string{wantAux}) || len(record.RollbackFailures) != 1 || record.RollbackFailures[0].Step != "retain-auxiliary:"+auxiliary {
					t.Fatalf("auxiliary recovery = %#v, %v", record, readErr)
				}
				if _, retryErr := value.Plan(context.Background(), request); retryErr == nil {
					t.Fatal("auxiliary recovery did not block retry")
				}
			})
		}
	}
}

func TestRepositoryBranchAuxiliaryAtomicOutcomeRecordsRollbackBoundary(t *testing.T) {
	for _, source := range []string{"conditional-restore", "displaced-expected-cleanup"} {
		for _, rollbackAt := range []int{3, 4} {
			t.Run(fmt.Sprintf("%s-rollback-%d", source, rollbackAt), func(t *testing.T) {
				project, root, backend, data := createFixture(t)
				backend.Run(t, "branch", "next")
				publishCompanionBaseline(t, project, root, "backend", "main", false)
				project = reloadCompanionFixtureProject(t, project, data)
				local, err := config.ReadProjectFile(project.ConfigPath)
				if err != nil {
					t.Fatal(err)
				}
				portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
				beforeLocal, beforePortable := mustRepositoryBranchRead(t, project.ConfigPath), mustRepositoryBranchRead(t, portablePath)
				lstatCountdown := 0
				lstat := func(path string) (os.FileInfo, error) {
					if lstatCountdown > 0 {
						lstatCountdown--
						if lstatCountdown == 0 {
							return nil, errors.New("injected final verification failure")
						}
					}
					return os.Lstat(path)
				}
				writes := 0
				var auxiliary string
				writer := func(path string, value []byte, mode os.FileMode, expected os.FileInfo, compare func() error) error {
					if err := compare(); err != nil {
						return err
					}
					if err := fsutil.WriteFileAtomicModeExpected(path, value, mode, expected); err != nil {
						return err
					}
					writes++
					if writes == 2 {
						lstatCountdown = 2
					}
					if writes == rollbackAt {
						auxiliary = path + ".retained-rollback"
						if err := os.WriteFile(auxiliary, []byte("foreign rollback auxiliary\n"), 0o600); err != nil {
							return err
						}
						return &fsutil.AuxiliaryOutcomeError{Paths: []string{auxiliary}, Err: fmt.Errorf("injected %s rollback auxiliary", source)}
					}
					return nil
				}
				value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, lstat, writer, store.WriteRecoveryCAS)
				_, err = value.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
				var application *service.Error
				if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
					t.Fatalf("rollback auxiliary error = %v", err)
				}
				if got := mustRepositoryBranchRead(t, auxiliary); !bytes.Equal(got, []byte("foreign rollback auxiliary\n")) {
					t.Fatalf("auxiliary lost: %q", got)
				}
				if !bytes.Equal(mustRepositoryBranchRead(t, project.ConfigPath), beforeLocal) || !bytes.Equal(mustRepositoryBranchRead(t, portablePath), beforePortable) {
					t.Fatal("rollback destinations not restored")
				}
				record, readErr := store.ReadRecovery(filepath.Join(data, "projects", project.ID, "recovery", "repo-branch-backend.json"))
				want := "auxiliary:" + auxiliary
				if readErr != nil || !reflect.DeepEqual(record.CompletedSteps, []string{"portable", "local"}) || !reflect.DeepEqual(record.UnrevertedSteps, []string{want}) {
					t.Fatalf("rollback auxiliary recovery=%#v err=%v", record, readErr)
				}
			})
		}
	}
}

func TestRepositoryBranchAuxiliaryRecoveryRecordFailuresPreserveForeignEvidence(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		recover func(string, store.RecoveryRecord, func() error) error
	}{
		{name: "writer", recover: func(string, store.RecoveryRecord, func() error) error {
			return errors.New("injected recovery writer failure")
		}},
		{name: "cas", recover: func(path string, _ store.RecoveryRecord, compare func() error) error {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte("foreign recovery record\n"), 0o600); err != nil {
				return err
			}
			return compare()
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			backend.Run(t, "branch", "next")
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			var auxiliary string
			writer := func(path string, value []byte, mode os.FileMode, expected os.FileInfo, compare func() error) error {
				if err := compare(); err != nil {
					return err
				}
				if err := fsutil.WriteFileAtomicModeExpected(path, value, mode, expected); err != nil {
					return err
				}
				auxiliary = path + ".recovery-failure-aux"
				if err := os.WriteFile(auxiliary, []byte("foreign auxiliary evidence\n"), 0o600); err != nil {
					return err
				}
				return &fsutil.AuxiliaryOutcomeError{Paths: []string{auxiliary}, Err: errors.New("retained auxiliary")}
			}
			value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Lstat, writer, scenario.recover)
			_, err := value.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
			var application *service.Error
			if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
				t.Fatalf("recovery outcome error = %v", err)
			}
			if got := mustRepositoryBranchRead(t, auxiliary); !bytes.Equal(got, []byte("foreign auxiliary evidence\n")) {
				t.Fatalf("auxiliary was altered: %q", got)
			}
			recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", "repo-branch-backend.json")
			if scenario.name == "cas" {
				if got := mustRepositoryBranchRead(t, recoveryPath); !bytes.Equal(got, []byte("foreign recovery record\n")) {
					t.Fatalf("foreign recovery overwritten: %q", got)
				}
			}
		})
	}
}

func TestRepositoryBranchAuxiliaryRollbackRecoveryRecordFailuresPreserveForeignEvidence(t *testing.T) {
	for _, source := range []string{"conditional-restore", "displaced-expected-cleanup"} {
		for _, scenario := range []struct {
			name    string
			recover func(string, store.RecoveryRecord, func() error) error
		}{
			{name: "writer", recover: func(string, store.RecoveryRecord, func() error) error {
				return errors.New("injected recovery writer failure")
			}},
			{name: "cas", recover: func(path string, _ store.RecoveryRecord, compare func() error) error {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte("foreign recovery record\n"), 0o600); err != nil {
					return err
				}
				return compare()
			}},
		} {
			t.Run(source+"-"+scenario.name, func(t *testing.T) {
				project, root, backend, data := createFixture(t)
				backend.Run(t, "branch", "next")
				publishCompanionBaseline(t, project, root, "backend", "main", false)
				project = reloadCompanionFixtureProject(t, project, data)
				local, err := config.ReadProjectFile(project.ConfigPath)
				if err != nil {
					t.Fatal(err)
				}
				portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
				beforeLocal, beforePortable := mustRepositoryBranchRead(t, project.ConfigPath), mustRepositoryBranchRead(t, portablePath)
				lstatCountdown := 0
				lstat := func(path string) (os.FileInfo, error) {
					if lstatCountdown > 0 {
						lstatCountdown--
						if lstatCountdown == 0 {
							return nil, errors.New("injected final verification failure")
						}
					}
					return os.Lstat(path)
				}
				writes := 0
				var auxiliary string
				writer := func(path string, value []byte, mode os.FileMode, expected os.FileInfo, compare func() error) error {
					if err := compare(); err != nil {
						return err
					}
					if err := fsutil.WriteFileAtomicModeExpected(path, value, mode, expected); err != nil {
						return err
					}
					writes++
					if writes == 2 {
						lstatCountdown = 2
					}
					if writes == 3 {
						auxiliary = path + ".rollback-recovery-failure-aux"
						if err := os.WriteFile(auxiliary, []byte("foreign rollback auxiliary evidence\n"), 0o600); err != nil {
							return err
						}
						return &fsutil.AuxiliaryOutcomeError{Paths: []string{auxiliary}, Err: fmt.Errorf("retained %s rollback auxiliary", source)}
					}
					return nil
				}
				value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, lstat, writer, scenario.recover)
				request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
				_, err = value.Execute(context.Background(), request)
				var application *service.Error
				if !errors.As(err, &application) || application.Kind != service.ErrorRollbackIncomplete {
					t.Fatalf("rollback recovery outcome error = %v", err)
				}
				if got := mustRepositoryBranchRead(t, auxiliary); !bytes.Equal(got, []byte("foreign rollback auxiliary evidence\n")) {
					t.Fatalf("auxiliary was altered: %q", got)
				}
				if !bytes.Equal(mustRepositoryBranchRead(t, project.ConfigPath), beforeLocal) || !bytes.Equal(mustRepositoryBranchRead(t, portablePath), beforePortable) {
					t.Fatal("rollback recovery outcome claimed a clean transaction")
				}
				recoveryPath := filepath.Join(data, "projects", project.ID, "recovery", "repo-branch-backend.json")
				if scenario.name == "cas" {
					if got := mustRepositoryBranchRead(t, recoveryPath); !bytes.Equal(got, []byte("foreign recovery record\n")) {
						t.Fatalf("foreign recovery overwritten: %q", got)
					}
					if _, retryErr := value.Plan(context.Background(), request); retryErr == nil {
						t.Fatal("foreign recovery record did not block retry")
					}
				}
			})
		}
	}
}

func TestRepositoryBranchExpectedFinalExchangePreservesForeignReplacement(t *testing.T) {
	for _, replaceAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("target-%d", replaceAt), func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			backend.Run(t, "branch", "next")
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			local, err := config.ReadProjectFile(project.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
			beforePortable := mustRepositoryBranchRead(t, portablePath)
			writes := 0
			writer := func(path string, value []byte, mode os.FileMode, expected os.FileInfo, compare func() error) error {
				writes++
				if err := compare(); err != nil {
					return err
				}
				if writes == replaceAt {
					foreign := path + ".foreign-replacement"
					if err := os.WriteFile(foreign, []byte("foreign final exchange generation\n"), mode); err != nil {
						return err
					}
					if err := os.Rename(foreign, path); err != nil {
						return err
					}
				}
				return fsutil.WriteFileAtomicModeExpected(path, value, mode, expected)
			}
			value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Lstat, writer, store.WriteRecoveryCAS)
			_, err = value.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
			if err == nil {
				t.Fatal("foreign final exchange replacement was overwritten")
			}
			path := portablePath
			if replaceAt == 2 {
				path = project.ConfigPath
				if !bytes.Equal(mustRepositoryBranchRead(t, portablePath), beforePortable) {
					t.Fatal("local final exchange failure did not restore portable generation")
				}
			}
			if got := mustRepositoryBranchRead(t, path); !bytes.Equal(got, []byte("foreign final exchange generation\n")) {
				t.Fatalf("foreign generation was overwritten: %q", got)
			}
		})
	}
}

func TestRepositoryBranchPostReplacementRollbackFailuresObserveRestoration(t *testing.T) {
	for _, rollbackAt := range []int{3, 4} {
		t.Run(fmt.Sprintf("rollback-dir-sync-%d", rollbackAt), func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			backend.Run(t, "branch", "next")
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			local, err := config.ReadProjectFile(project.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
			beforeLocal, beforePortable := mustRepositoryBranchRead(t, project.ConfigPath), mustRepositoryBranchRead(t, portablePath)
			lstatBeforeVerifyFailure := 0
			lstat := func(path string) (os.FileInfo, error) {
				if lstatBeforeVerifyFailure > 0 {
					lstatBeforeVerifyFailure--
					if lstatBeforeVerifyFailure == 0 {
						return nil, errors.New("injected final verification failure")
					}
				}
				return os.Lstat(path)
			}
			writes := 0
			writer := func(path string, value []byte, mode os.FileMode, _ os.FileInfo, compare func() error) error {
				writes++
				if err := compare(); err != nil {
					return err
				}
				err := fsutil.WriteFileAtomicModeWithHook(path, value, mode, func(step string) error {
					if writes == rollbackAt && step == "dir-sync" {
						return errors.New("injected post-replacement rollback sync failure")
					}
					return nil
				})
				if writes == 2 {
					// First lstat observes the just-published local file; the
					// second is the final portable verification boundary.
					lstatBeforeVerifyFailure = 2
				}
				return err
			}
			value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, lstat, writer, store.WriteRecoveryCAS)
			_, err = value.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
			if err == nil || !service.HasCleanRollback(err) {
				t.Fatalf("post-replacement rollback error = %v", err)
			}
			if gotLocal, gotPortable := mustRepositoryBranchRead(t, project.ConfigPath), mustRepositoryBranchRead(t, portablePath); !bytes.Equal(gotLocal, beforeLocal) || !bytes.Equal(gotPortable, beforePortable) {
				t.Fatalf("post-replacement rollback did not restore both generations: writes=%d local=%q portable=%q", writes, gotLocal, gotPortable)
			}
		})
	}
}

func TestRepositoryBranchCompareFailuresAtBothPublicationBoundariesRollBack(t *testing.T) {
	for _, failure := range []int{1, 2} {
		t.Run(fmt.Sprintf("compare-%d", failure), func(t *testing.T) {
			project, root, backend, data := createFixture(t)
			backend.Run(t, "branch", "next")
			publishCompanionBaseline(t, project, root, "backend", "main", false)
			project = reloadCompanionFixtureProject(t, project, data)
			local, err := config.ReadProjectFile(project.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
			beforeLocal, beforePortable := mustRepositoryBranchRead(t, project.ConfigPath), mustRepositoryBranchRead(t, portablePath)
			calls := 0
			writer := func(path string, value []byte, mode os.FileMode, _ os.FileInfo, compare func() error) error {
				calls++
				if calls == failure {
					return errors.New("injected CAS comparison failure")
				}
				if err := compare(); err != nil {
					return err
				}
				return fsutil.WriteFileAtomicMode(path, value, mode)
			}
			value := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Stat, writer, store.WriteRecoveryCAS)
			_, err = value.Execute(context.Background(), service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"})
			if err == nil || !service.HasCleanRollback(err) {
				t.Fatalf("compare failure = %v", err)
			}
			if got := mustRepositoryBranchRead(t, project.ConfigPath); !bytes.Equal(got, beforeLocal) {
				t.Fatal("compare rollback changed local")
			}
			if got := mustRepositoryBranchRead(t, portablePath); !bytes.Equal(got, beforePortable) {
				t.Fatal("compare rollback changed portable")
			}
		})
	}
}

func TestRepositoryBranchCancellationAfterLockAndPublicationBoundaries(t *testing.T) {
	project, root, backend, data := createFixture(t)
	backend.Run(t, "branch", "next")
	publishCompanionBaseline(t, project, root, "backend", "main", false)
	project = reloadCompanionFixtureProject(t, project, data)
	request := service.RepositoryBranchRequest{Project: project, DataDir: data, RepositoryID: "backend", Branch: "next"}
	plan, err := service.NewRepositoryBranchService().Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	local, err := config.ReadProjectFile(project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	portablePath := filepath.Join(filepath.Dir(project.ConfigPath), local.Manifest.Path)
	beforeLocal, beforePortable := mustRepositoryBranchRead(t, project.ConfigPath), mustRepositoryBranchRead(t, portablePath)

	ctx, cancel := context.WithCancel(context.Background())
	locked := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), repositoryBranchCancelingLocker{cancel: cancel}, os.ReadFile, os.Stat, nil, store.WriteRecoveryCAS)
	if _, err := locked.ExecutePlan(ctx, request, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation after lock = %v", err)
	}
	if !bytes.Equal(mustRepositoryBranchRead(t, project.ConfigPath), beforeLocal) || !bytes.Equal(mustRepositoryBranchRead(t, portablePath), beforePortable) {
		t.Fatal("cancellation after lock wrote configuration")
	}

	ctx, cancel = context.WithCancel(context.Background())
	writes := 0
	writer := func(path string, value []byte, mode os.FileMode, _ os.FileInfo, compare func() error) error {
		writes++
		if err := compare(); err != nil {
			return err
		}
		if err := fsutil.WriteFileAtomicMode(path, value, mode); err != nil {
			return err
		}
		if writes == 1 {
			cancel()
		}
		return nil
	}
	published := service.NewRepositoryBranchServiceWith(gitadapter.NewAdapter("git"), lock.Manager{}, os.ReadFile, os.Stat, writer, store.WriteRecoveryCAS)
	if _, err := published.ExecutePlan(ctx, request, plan); !errors.Is(err, context.Canceled) || !service.HasCleanRollback(err) {
		t.Fatalf("cancellation after first publication = %v", err)
	}
	if !bytes.Equal(mustRepositoryBranchRead(t, project.ConfigPath), beforeLocal) || !bytes.Equal(mustRepositoryBranchRead(t, portablePath), beforePortable) {
		t.Fatal("cancellation after first publication did not cleanly restore")
	}
}

type repositoryBranchCancelingLocker struct{ cancel context.CancelFunc }

func (locker repositoryBranchCancelingLocker) ProjectLock(ctx context.Context, dataDir, projectID string, timeout time.Duration) (lock.Handle, error) {
	handle, err := (lock.Manager{}).ProjectLock(ctx, dataDir, projectID, timeout)
	if err == nil {
		locker.cancel()
	}
	return handle, err
}

func mustRepositoryBranchRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
