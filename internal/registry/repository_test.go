package registry

import (
	"strings"
	"testing"
)

func TestRepositoryPathValidation(t *testing.T) {
	for _, repo := range []string{
		"app",
		"team/nested/app",
		"example.test/app",
		"team_name/app__name",
		"team/app---name",
		strings.Repeat("a", 255),
	} {
		if !validRepositoryPath(repo) {
			t.Errorf("rejected valid repository %q", repo)
		}
	}
	for _, repo := range []string{
		"",
		"/app",
		"app/",
		"team//app",
		"UPPER/app",
		"team/app..name",
		"team:5000/app",
		"[::1]/app",
		"team/app:latest",
		"team/app@sha256:" + strings.Repeat("a", 64),
		strings.Repeat("a", 256),
	} {
		if validRepositoryPath(repo) {
			t.Errorf("accepted invalid repository %q", repo)
		}
	}
}
