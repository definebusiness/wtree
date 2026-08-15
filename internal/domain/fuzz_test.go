package domain_test

import (
	"testing"

	"github.com/definebusiness/wtree/internal/domain"
)

func FuzzProjectValidate(f *testing.F) {
	for _, seed := range [][3]string{{"root", "child", "api"}, {"root", "root", "../escape"}, {"α", "β", "nested"}} {
		f.Add(seed[0], seed[1], seed[2])
	}
	f.Fuzz(func(t *testing.T, rootID, childID, mount string) {
		project := domain.Project{Version: domain.CurrentVersion, ID: "project", Repositories: []domain.Repository{{ID: rootID, DefaultMount: "."}, {ID: childID, ParentID: rootID, DefaultMount: mount}}}
		_ = project.Validate()
	})
}
