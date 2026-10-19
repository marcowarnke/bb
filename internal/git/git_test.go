package git

import "testing"

func TestParseRemoteURL(t *testing.T) {
	cases := map[string][3]string{
		"https://bitbucket.corp/scm/proj/repo.git":               {"bitbucket.corp", "PROJ", "repo"},
		"https://user@bitbucket.corp/ctx/scm/~jdoe/Repo.git":     {"bitbucket.corp", "~jdoe", "repo"},
		"ssh://git@bitbucket.corp:7999/proj/repo.git":            {"bitbucket.corp", "PROJ", "repo"},
		"git@bitbucket.corp:proj/repo.git":                       {"bitbucket.corp", "PROJ", "repo"},
		"https://bitbucket.corp/projects/PROJ/repos/repo/browse": {"bitbucket.corp", "PROJ", "repo"},
		"https://bitbucket.corp/users/jdoe/repos/repo/browse":    {"bitbucket.corp", "~jdoe", "repo"},
	}
	for in, want := range cases {
		h, p, s, ok := ParseRemoteURL(in)
		if !ok || [3]string{h, p, s} != want {
			t.Errorf("%s: got %v %v %v %v, want %v", in, h, p, s, ok, want)
		}
	}
	if _, _, _, ok := ParseRemoteURL("/local/path"); ok {
		t.Error("local path should not parse")
	}
}
