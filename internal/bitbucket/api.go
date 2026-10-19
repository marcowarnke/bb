package bitbucket

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type User struct {
	ID           int    `json:"id,omitempty"`
	Name         string `json:"name"`
	Slug         string `json:"slug,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
	EmailAddress string `json:"emailAddress,omitempty"`
	Active       bool   `json:"active,omitempty"`
}

type Project struct {
	ID   int    `json:"id,omitempty"`
	Key  string `json:"key"`
	Name string `json:"name,omitempty"`
}

type Link struct {
	Href string `json:"href"`
	Name string `json:"name,omitempty"`
}

type Repository struct {
	ID          int               `json:"id,omitempty"`
	Slug        string            `json:"slug"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Public      bool              `json:"public,omitempty"`
	Project     Project           `json:"project"`
	Origin      *Repository       `json:"origin,omitempty"`
	Links       map[string][]Link `json:"links,omitempty"`
}

func (r Repository) FullName() string { return r.Project.Key + "/" + r.Slug }

func (r Repository) WebURL() string {
	if l := r.Links["self"]; len(l) > 0 {
		return l[0].Href
	}
	return ""
}

func (r Repository) CloneURL(name string) string {
	for _, l := range r.Links["clone"] {
		if l.Name == name {
			return l.Href
		}
	}
	return ""
}

type Ref struct {
	ID           string      `json:"id"`
	DisplayID    string      `json:"displayId,omitempty"`
	LatestCommit string      `json:"latestCommit,omitempty"`
	Repository   *Repository `json:"repository,omitempty"`
}

type Participant struct {
	User               User   `json:"user"`
	Role               string `json:"role,omitempty"`
	Approved           bool   `json:"approved,omitempty"`
	Status             string `json:"status,omitempty"`
	LastReviewedCommit string `json:"lastReviewedCommit,omitempty"`
}

type PullRequest struct {
	ID           int               `json:"id"`
	Version      int               `json:"version"`
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	State        string            `json:"state"`
	Open         bool              `json:"open"`
	Closed       bool              `json:"closed"`
	Draft        bool              `json:"draft"`
	CreatedDate  int64             `json:"createdDate"`
	UpdatedDate  int64             `json:"updatedDate"`
	ClosedDate   int64             `json:"closedDate"`
	FromRef      Ref               `json:"fromRef"`
	ToRef        Ref               `json:"toRef"`
	Author       Participant       `json:"author"`
	Reviewers    []Participant     `json:"reviewers"`
	Participants []Participant     `json:"participants"`
	Links        map[string][]Link `json:"links"`
	Properties   struct {
		CommentCount  int `json:"commentCount"`
		OpenTaskCount int `json:"openTaskCount"`
	} `json:"properties"`
}

func (p PullRequest) WebURL() string {
	if l := p.Links["self"]; len(l) > 0 {
		return l[0].Href
	}
	return ""
}

func (p PullRequest) IsCrossRepository() bool {
	return p.FromRef.Repository != nil && p.ToRef.Repository != nil && p.FromRef.Repository.ID != p.ToRef.Repository.ID
}

type Anchor struct {
	Path     string `json:"path,omitempty"`
	SrcPath  string `json:"srcPath,omitempty"`
	Line     int    `json:"line,omitempty"`
	LineType string `json:"lineType,omitempty"`
	FileType string `json:"fileType,omitempty"`
	DiffType string `json:"diffType,omitempty"`
	FromHash string `json:"fromHash,omitempty"`
	ToHash   string `json:"toHash,omitempty"`
	Orphaned bool   `json:"orphaned,omitempty"`
}

type Comment struct {
	ID             int       `json:"id"`
	Version        int       `json:"version"`
	Text           string    `json:"text"`
	Author         User      `json:"author"`
	CreatedDate    int64     `json:"createdDate"`
	UpdatedDate    int64     `json:"updatedDate"`
	Comments       []Comment `json:"comments"`
	Severity       string    `json:"severity,omitempty"`
	State          string    `json:"state,omitempty"`
	ThreadResolved bool      `json:"threadResolved,omitempty"`
	Anchor         *Anchor   `json:"anchor,omitempty"`
}

type Activity struct {
	ID            int      `json:"id"`
	Action        string   `json:"action"`
	CommentAction string   `json:"commentAction"`
	CreatedDate   int64    `json:"createdDate"`
	User          User     `json:"user"`
	Comment       *Comment `json:"comment"`
	CommentAnchor *Anchor  `json:"commentAnchor"`
}

type Change struct {
	Type string `json:"type"`
	Path struct {
		ToString string `json:"toString"`
	} `json:"path"`
	SrcPath *struct {
		ToString string `json:"toString"`
	} `json:"srcPath"`
}

type BuildStatus struct {
	State       string `json:"state"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	DateAdded   int64  `json:"dateAdded"`
}

type MergeStatus struct {
	CanMerge   bool   `json:"canMerge"`
	Conflicted bool   `json:"conflicted"`
	Outcome    string `json:"outcome"`
	Vetoes     []struct {
		SummaryMessage  string `json:"summaryMessage"`
		DetailedMessage string `json:"detailedMessage"`
	} `json:"vetoes"`
}

// RepoPath returns the REST path prefix for a repository.
func RepoPath(project, slug string) string {
	return "rest/api/1.0/projects/" + url.PathEscape(project) + "/repos/" + url.PathEscape(slug)
}

func PRPath(project, slug string, id int) string {
	return RepoPath(project, slug) + "/pull-requests/" + strconv.Itoa(id)
}

func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.StatusCode == http.StatusNotFound
}

func (c *Client) GetRepo(project, slug string) (*Repository, error) {
	var r Repository
	return &r, c.Do("GET", RepoPath(project, slug), nil, nil, &r)
}

func (c *Client) DefaultBranch(project, slug string) (string, error) {
	var ref Ref
	err := c.Do("GET", RepoPath(project, slug)+"/default-branch", nil, nil, &ref)
	if IsNotFound(err) {
		// Pre-7.x endpoint.
		err = c.Do("GET", RepoPath(project, slug)+"/branches/default", nil, nil, &ref)
	}
	if err != nil {
		return "", err
	}
	return ref.DisplayID, nil
}

func (c *Client) BranchExists(project, slug, branch string) (bool, error) {
	q := url.Values{"filterText": {branch}, "boostMatches": {"true"}}
	found, err := Paginate(c, RepoPath(project, slug)+"/branches", q, 1, func(r Ref) bool { return r.DisplayID == branch })
	return len(found) > 0, err
}

func (c *Client) GetPR(project, slug string, id int) (*PullRequest, error) {
	var pr PullRequest
	return &pr, c.Do("GET", PRPath(project, slug, id), nil, nil, &pr)
}

// ResolveUser maps a user-supplied identifier (slug, username, email, display
// name fragment) to a Bitbucket username.
func (c *Client) ResolveUser(q string) (*User, error) {
	q = strings.TrimPrefix(q, "@")
	var u User
	err := c.Do("GET", "rest/api/1.0/users/"+url.PathEscape(q), nil, nil, &u)
	if err == nil {
		return &u, nil
	}
	if !IsNotFound(err) {
		return nil, err
	}
	users, err := Paginate[User](c, "rest/api/1.0/users", url.Values{"filter": {q}}, 25, nil)
	if err != nil {
		return nil, err
	}
	for _, cand := range users {
		if strings.EqualFold(cand.Name, q) || strings.EqualFold(cand.Slug, q) || strings.EqualFold(cand.EmailAddress, q) {
			return &cand, nil
		}
	}
	switch len(users) {
	case 0:
		return nil, fmt.Errorf("no user matching %q", q)
	case 1:
		return &users[0], nil
	}
	var names []string
	for _, cand := range users {
		names = append(names, fmt.Sprintf("%s (%s)", cand.Slug, cand.DisplayName))
	}
	return nil, fmt.Errorf("%q is ambiguous, candidates: %s", q, strings.Join(names, ", "))
}

// Comments returns the PR's comment threads (root comments with nested
// replies), oldest first, with anchors filled in from activities.
func (c *Client) Comments(project, slug string, id int) ([]Comment, error) {
	acts, err := Paginate(c, PRPath(project, slug, id)+"/activities", nil, 0, func(a Activity) bool {
		return a.Action == "COMMENTED" && a.Comment != nil
	})
	if err != nil {
		return nil, err
	}
	nested := map[int]bool{}
	var mark func(cs []Comment)
	mark = func(cs []Comment) {
		for _, cm := range cs {
			nested[cm.ID] = true
			mark(cm.Comments)
		}
	}
	for _, a := range acts {
		mark(a.Comment.Comments)
	}
	seen := map[int]bool{}
	var roots []Comment
	// Activities are newest first; walk backwards so roots end up oldest first,
	// but keep the newest copy of each root (it has the latest replies/edits).
	latest := map[int]Comment{}
	for _, a := range acts {
		if _, ok := latest[a.Comment.ID]; !ok {
			cm := *a.Comment
			if cm.Anchor == nil {
				cm.Anchor = a.CommentAnchor
			}
			latest[cm.ID] = cm
		}
	}
	for i := len(acts) - 1; i >= 0; i-- {
		id := acts[i].Comment.ID
		if nested[id] || seen[id] {
			continue
		}
		seen[id] = true
		roots = append(roots, latest[id])
	}
	return roots, nil
}

func (c *Client) Changes(project, slug string, id int) ([]Change, error) {
	return Paginate[Change](c, PRPath(project, slug, id)+"/changes", url.Values{"withComments": {"false"}}, 0, nil)
}

func (c *Client) BuildStatuses(commit string) ([]BuildStatus, error) {
	return Paginate[BuildStatus](c, "rest/build-status/1.0/commits/"+url.PathEscape(commit), nil, 0, nil)
}

func (c *Client) MergeStatus(project, slug string, id int) (*MergeStatus, error) {
	var m MergeStatus
	return &m, c.Do("GET", PRPath(project, slug, id)+"/merge", nil, nil, &m)
}

// RawDiff returns the PR's unified diff. Falls back to rendering the JSON diff
// on servers without the .diff endpoint.
func (c *Client) RawDiff(project, slug string, id int, patch bool) (string, error) {
	suffix := ".diff"
	if patch {
		suffix = ".patch"
	}
	resp, err := c.Raw("GET", PRPath(project, slug, id)+suffix, nil, http.Header{"Accept": {"text/plain"}}, nil)
	if err == nil {
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		return string(data), err
	}
	if !IsNotFound(err) || patch {
		return "", err
	}
	d, err := c.PRDiff(project, slug, id, "", 3)
	if err != nil {
		return "", err
	}
	return d.Unified(), nil
}
