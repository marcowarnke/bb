package bitbucket

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DiffResponse is Bitbucket's structured (JSON) diff.
type DiffResponse struct {
	FromHash  string `json:"fromHash"`
	ToHash    string `json:"toHash"`
	Truncated bool   `json:"truncated"`
	Diffs     []Diff `json:"diffs"`
}

type DiffPath struct {
	ToString string `json:"toString"`
}

type Diff struct {
	Source      *DiffPath `json:"source"`
	Destination *DiffPath `json:"destination"`
	Binary      bool      `json:"binary"`
	Truncated   bool      `json:"truncated"`
	Hunks       []Hunk    `json:"hunks"`
}

// Path is the file's current path (or its old path if deleted).
func (d Diff) Path() string {
	if d.Destination != nil {
		return d.Destination.ToString
	}
	if d.Source != nil {
		return d.Source.ToString
	}
	return ""
}

// OldPath is the file's previous path, if it existed.
func (d Diff) OldPath() string {
	if d.Source != nil {
		return d.Source.ToString
	}
	return ""
}

func (d Diff) ChangeType() string {
	switch {
	case d.Source == nil:
		return "ADD"
	case d.Destination == nil:
		return "DELETE"
	case d.Source.ToString != d.Destination.ToString:
		return "RENAME"
	}
	return "MODIFY"
}

type Hunk struct {
	SourceLine      int       `json:"sourceLine"`
	SourceSpan      int       `json:"sourceSpan"`
	DestinationLine int       `json:"destinationLine"`
	DestinationSpan int       `json:"destinationSpan"`
	Truncated       bool      `json:"truncated"`
	Segments        []Segment `json:"segments"`
}

// Segment is a run of ADDED, REMOVED or CONTEXT lines.
type Segment struct {
	Type      string     `json:"type"`
	Truncated bool       `json:"truncated"`
	Lines     []DiffLine `json:"lines"`
}

// DiffLine carries both line numbers; Source is meaningful for REMOVED and
// CONTEXT lines, Destination for ADDED and CONTEXT lines.
type DiffLine struct {
	Source      int    `json:"source"`
	Destination int    `json:"destination"`
	Line        string `json:"line"`
	Truncated   bool   `json:"truncated"`
}

func pathSuffix(path string) string {
	if path == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/" + strings.Join(parts, "/")
}

func diffQuery(contextLines int) url.Values {
	q := url.Values{"withComments": {"false"}}
	if contextLines >= 0 {
		q.Set("contextLines", strconv.Itoa(contextLines))
	}
	return q
}

// PRDiff fetches the PR's structured diff, optionally for a single path.
// contextLines < 0 uses the server default, which is also what decides which
// lines can carry comments.
func (c *Client) PRDiff(project, slug string, id int, path string, contextLines int) (*DiffResponse, error) {
	var d DiffResponse
	return &d, c.Do("GET", PRPath(project, slug, id)+"/diff"+pathSuffix(path), diffQuery(contextLines), nil, &d)
}

// CompareDiff fetches the diff of changes in from that are not in to.
func (c *Client) CompareDiff(project, slug, from, to, path string, contextLines int) (*DiffResponse, error) {
	q := diffQuery(contextLines)
	q.Set("from", from)
	q.Set("to", to)
	var d DiffResponse
	return &d, c.Do("GET", RepoPath(project, slug)+"/compare/diff"+pathSuffix(path), q, nil, &d)
}

// Unified renders the structured diff in git's unified format.
func (d *DiffResponse) Unified() string {
	var b strings.Builder
	for _, f := range d.Diffs {
		f.writeUnified(&b)
	}
	return b.String()
}

func (f Diff) writeUnified(b *strings.Builder) {
	src, dst := "/dev/null", "/dev/null"
	if f.Source != nil {
		src = "a/" + f.Source.ToString
	}
	if f.Destination != nil {
		dst = "b/" + f.Destination.ToString
	}
	old := f.OldPath()
	if old == "" {
		old = f.Path()
	}
	fmt.Fprintf(b, "diff --git a/%s b/%s\n", old, f.Path())
	if f.Binary {
		fmt.Fprintf(b, "Binary files %s and %s differ\n", src, dst)
		return
	}
	fmt.Fprintf(b, "--- %s\n+++ %s\n", src, dst)
	for _, h := range f.Hunks {
		fmt.Fprintf(b, "@@ -%d,%d +%d,%d @@\n", h.SourceLine, h.SourceSpan, h.DestinationLine, h.DestinationSpan)
		for _, s := range h.Segments {
			prefix := " "
			switch s.Type {
			case "ADDED":
				prefix = "+"
			case "REMOVED":
				prefix = "-"
			}
			for _, l := range s.Lines {
				b.WriteString(prefix + l.Line + "\n")
			}
		}
	}
}

type Commit struct {
	ID              string `json:"id"`
	DisplayID       string `json:"displayId"`
	Message         string `json:"message"`
	AuthorTimestamp int64  `json:"authorTimestamp"`
	Author          struct {
		Name         string `json:"name"`
		EmailAddress string `json:"emailAddress"`
	} `json:"author"`
}

func (c *Client) PRCommits(project, slug string, id int) ([]Commit, error) {
	return Paginate[Commit](c, PRPath(project, slug, id)+"/commits", nil, 0, nil)
}

func (c *Client) GetComment(project, slug string, prID, commentID int) (*Comment, error) {
	var cm Comment
	return &cm, c.Do("GET", PRPath(project, slug, prID)+"/comments/"+strconv.Itoa(commentID), nil, nil, &cm)
}

// RawFile returns a file's content at a commit.
func (c *Client) RawFile(project, slug, path, at string) (string, error) {
	q := url.Values{}
	if at != "" {
		q.Set("at", at)
	}
	resp, err := c.Raw("GET", RepoPath(project, slug)+"/raw"+pathSuffix(path), q, http.Header{"Accept": {"text/plain"}}, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return string(data), err
}
