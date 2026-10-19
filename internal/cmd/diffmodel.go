package cmd

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/marcowarnke/bb/internal/bitbucket"
)

// pathFilter selects files by glob: patterns with a "/" are anchored at the
// repo root ("src/api" and "src/api/" match that directory), bare names like
// "*.lock" match at any depth; "*" stays within a path segment, "**" crosses.
type pathFilter struct {
	include, exclude []*regexp.Regexp
}

func newPathFilter(include, exclude []string) pathFilter {
	var f pathFilter
	for _, p := range include {
		f.include = append(f.include, globRegexp(p))
	}
	for _, p := range splitList(exclude) {
		f.exclude = append(f.exclude, globRegexp(p))
	}
	return f
}

func globRegexp(p string) *regexp.Regexp {
	p = strings.TrimPrefix(p, "./")
	var b strings.Builder
	b.WriteString("^")
	if !strings.Contains(p, "/") {
		b.WriteString("(.*/)?") // a bare name like "*.lock" matches at any depth
	}
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '*' && i+1 < len(p) && p[i+1] == '*':
			b.WriteString(".*")
			i++
			if i+1 < len(p) && p[i+1] == '/' {
				i++
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	// A bare name also matches everything below it (directory prefix).
	if !strings.HasSuffix(p, "/") {
		b.WriteString("(/.*)?")
	} else {
		b.WriteString(".*")
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func (f pathFilter) empty() bool { return len(f.include) == 0 && len(f.exclude) == 0 }

func (f pathFilter) match(paths ...string) bool {
	hit := func(res []*regexp.Regexp) bool {
		for _, re := range res {
			for _, p := range paths {
				if p != "" && re.MatchString(p) {
					return true
				}
			}
		}
		return false
	}
	if len(f.include) > 0 && !hit(f.include) {
		return false
	}
	return !hit(f.exclude)
}

func (f pathFilter) diffs(ds []bitbucket.Diff) []bitbucket.Diff {
	if f.empty() {
		return ds
	}
	var out []bitbucket.Diff
	for _, d := range ds {
		if f.match(d.Path(), d.OldPath()) {
			out = append(out, d)
		}
	}
	return out
}

// rawSection is one file's chunk of a unified diff.
type rawSection struct {
	path, oldPath, text string
}

var diffHeader = regexp.MustCompile(`^diff --git a/(.*) b/(.*)$`)

func splitRawDiff(d string) []rawSection {
	var out []rawSection
	for _, chunk := range strings.SplitAfter(d, "\n") {
		if m := diffHeader.FindStringSubmatch(strings.TrimRight(chunk, "\n")); m != nil {
			out = append(out, rawSection{oldPath: m[1], path: m[2]})
		}
		if len(out) == 0 {
			out = append(out, rawSection{})
		}
		out[len(out)-1].text += chunk
	}
	return out
}

// limitSections emits whole files until maxLines would be exceeded and
// reports the rest; the first file is cut rather than dropped.
func limitSections(secs []rawSection, maxLines int) (string, []string) {
	var b strings.Builder
	var omitted []string
	used := 0
	for _, s := range secs {
		n := strings.Count(s.text, "\n")
		switch {
		case maxLines <= 0 || used+n <= maxLines:
			b.WriteString(s.text)
			used += n
		case used == 0:
			lines := strings.SplitAfter(s.text, "\n")
			b.WriteString(strings.Join(lines[:maxLines], ""))
			used = maxLines
			omitted = append(omitted, s.path+" (partially shown)")
		default:
			omitted = append(omitted, s.path)
		}
	}
	return b.String(), omitted
}

const numberedLegend = "# Columns: OLD NEW MARK text. Comment with --line NEW for '+' and ' ' lines, --line OLD --line-type removed for '-' lines.\n"

// numbered renders one file with explicit old/new line numbers.
func numbered(f bitbucket.Diff) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s", f.Path())
	switch f.ChangeType() {
	case "ADD":
		b.WriteString(" (added)")
	case "DELETE":
		b.WriteString(" (deleted)")
	case "RENAME":
		fmt.Fprintf(&b, " (renamed from %s)", f.OldPath())
	}
	b.WriteString("\n")
	if f.Binary {
		b.WriteString("(binary file)\n")
		return b.String()
	}
	for _, h := range f.Hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.SourceLine, h.SourceSpan, h.DestinationLine, h.DestinationSpan)
		for _, s := range h.Segments {
			for _, l := range s.Lines {
				oldN, newN, mark := strconv.Itoa(l.Source), strconv.Itoa(l.Destination), " "
				switch s.Type {
				case "ADDED":
					oldN, mark = "", "+"
				case "REMOVED":
					newN, mark = "", "-"
				}
				fmt.Fprintf(&b, "%5s %5s %s %s\n", oldN, newN, mark, l.Line)
			}
		}
		if h.Truncated {
			b.WriteString("(hunk truncated by server)\n")
		}
	}
	if f.Truncated {
		b.WriteString("(file diff truncated by server)\n")
	}
	return b.String()
}

func diffStat(f bitbucket.Diff) (added, removed int) {
	for _, h := range f.Hunks {
		for _, s := range h.Segments {
			switch s.Type {
			case "ADDED":
				added += len(s.Lines)
			case "REMOVED":
				removed += len(s.Lines)
			}
		}
	}
	return
}

// diffJSON is the agent-facing structured diff.
func diffJSON(f bitbucket.Diff) map[string]any {
	added, removed := diffStat(f)
	var hunks []map[string]any
	for _, h := range f.Hunks {
		var lines []map[string]any
		for _, s := range h.Segments {
			for _, l := range s.Lines {
				line := map[string]any{"type": strings.ToLower(s.Type), "text": l.Line, "old": nil, "new": nil}
				if s.Type != "ADDED" {
					line["old"] = l.Source
				}
				if s.Type != "REMOVED" {
					line["new"] = l.Destination
				}
				lines = append(lines, line)
			}
		}
		hunks = append(hunks, map[string]any{
			"oldStart": h.SourceLine, "oldLines": h.SourceSpan, "newStart": h.DestinationLine, "newLines": h.DestinationSpan,
			"truncated": h.Truncated, "lines": lines,
		})
	}
	m := map[string]any{
		"path": f.Path(), "changeType": f.ChangeType(), "binary": f.Binary, "truncated": f.Truncated,
		"additions": added, "deletions": removed, "hunks": hunks,
	}
	if old := f.OldPath(); old != "" && old != f.Path() {
		m["oldPath"] = old
	}
	return m
}

// anchorFor validates that a comment can be attached to line (new-file number,
// or old-file number for removed lines) and builds the Bitbucket anchor.
// lineType "" infers ADDED or CONTEXT from the diff.
func anchorFor(f *bitbucket.Diff, path string, line int, lineType string) (map[string]any, error) {
	if f == nil {
		return nil, fmt.Errorf("%s is not changed in this pull request", path)
	}
	anchor := map[string]any{"path": f.Path(), "diffType": "EFFECTIVE"}
	if old := f.OldPath(); old != "" && old != f.Path() {
		anchor["srcPath"] = old
	}
	if line <= 0 {
		return anchor, nil // file-level comment
	}
	lineType = strings.ToUpper(lineType)
	var newLines, oldLines []int
	found := ""
	for _, h := range f.Hunks {
		for _, s := range h.Segments {
			for _, l := range s.Lines {
				if s.Type == "REMOVED" {
					oldLines = append(oldLines, l.Source)
					if lineType == "REMOVED" && l.Source == line {
						found = s.Type
					}
					continue
				}
				newLines = append(newLines, l.Destination)
				if l.Destination == line && (lineType == "" || lineType == s.Type) && found == "" {
					found = s.Type
				}
			}
		}
	}
	if found == "" {
		var b strings.Builder
		if lineType == "REMOVED" {
			fmt.Fprintf(&b, "%s: old line %d is not a removed line in this diff", path, line)
		} else {
			fmt.Fprintf(&b, "%s: new line %d is not part of this diff", path, line)
			if lineType != "" {
				fmt.Fprintf(&b, " as a %s line", strings.ToLower(lineType))
			}
		}
		fmt.Fprintf(&b, "\n  commentable new-file lines (--line N): %s", ranges(newLines))
		if len(oldLines) > 0 {
			fmt.Fprintf(&b, "\n  removed old-file lines (--line N --line-type removed): %s", ranges(oldLines))
		}
		b.WriteString("\n  (see `bb pr diff <pr> -n -- " + path + "`)")
		return nil, fmt.Errorf("%s", b.String())
	}
	fileType := "TO"
	if found == "REMOVED" {
		fileType = "FROM"
	}
	anchor["line"], anchor["lineType"], anchor["fileType"] = line, found, fileType
	return anchor, nil
}

// ranges compresses line numbers: [1 2 3 7 9 10] → "1-3, 7, 9-10".
func ranges(ns []int) string {
	if len(ns) == 0 {
		return "(none)"
	}
	sort.Ints(ns)
	var parts []string
	start, prev := ns[0], ns[0]
	flush := func() {
		if start == prev {
			parts = append(parts, strconv.Itoa(start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", start, prev))
		}
	}
	for _, n := range ns[1:] {
		if n == prev || n == prev+1 {
			prev = n
			continue
		}
		flush()
		start, prev = n, n
	}
	flush()
	return strings.Join(parts, ", ")
}

// diffCache fetches per-file PR diffs at the server's default context, which
// is what Bitbucket uses to decide where comments can go.
type diffCache struct {
	c     *bitbucket.Client
	r     Repo
	prID  int
	files map[string]*bitbucket.Diff
}

func newDiffCache(c *bitbucket.Client, r Repo, prID int) *diffCache {
	return &diffCache{c: c, r: r, prID: prID, files: map[string]*bitbucket.Diff{}}
}

func (dc *diffCache) file(path string) (*bitbucket.Diff, error) {
	if f, ok := dc.files[path]; ok {
		return f, nil
	}
	d, err := dc.c.PRDiff(dc.r.Project, dc.r.Slug, dc.prID, path, -1)
	if err != nil && !bitbucket.IsNotFound(err) {
		return nil, err
	}
	var f *bitbucket.Diff
	if d != nil {
		for i := range d.Diffs {
			if d.Diffs[i].Path() == path || d.Diffs[i].OldPath() == path {
				f = &d.Diffs[i]
				break
			}
		}
	}
	dc.files[path] = f
	return f, nil
}

// commentPayload builds and validates one comment.
func (dc *diffCache) commentPayload(spec commentSpec) (map[string]any, error) {
	body := strings.TrimSpace(spec.Body)
	if spec.Suggestion != nil {
		if spec.Path == "" || spec.Line <= 0 || strings.EqualFold(spec.LineType, "removed") {
			return nil, fmt.Errorf("a suggestion needs --path and --line on a new-file line")
		}
		body += "\n\n```suggestion\n" + strings.TrimRight(*spec.Suggestion, "\n") + "\n```"
	}
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("comment body is empty")
	}
	if spec.Line > 0 && spec.Path == "" {
		return nil, fmt.Errorf("line %d given without a path", spec.Line)
	}
	if lt := strings.ToLower(spec.LineType); lt != "" && lt != "added" && lt != "removed" && lt != "context" {
		return nil, fmt.Errorf("invalid line type %q: valid values are {added|removed|context}", spec.LineType)
	}
	payload := map[string]any{"text": body}
	if spec.Task {
		payload["severity"] = "BLOCKER"
	}
	if spec.ReplyTo > 0 {
		payload["parent"] = map[string]any{"id": spec.ReplyTo}
		return payload, nil
	}
	if spec.Path != "" {
		f, err := dc.file(spec.Path)
		if err != nil {
			return nil, err
		}
		anchor, err := anchorFor(f, spec.Path, spec.Line, spec.LineType)
		if err != nil {
			return nil, err
		}
		payload["anchor"] = anchor
	}
	return payload, nil
}

// commentSpec is one comment, from flags or a review file.
type commentSpec struct {
	Path       string  `json:"path"`
	Line       int     `json:"line"`
	LineType   string  `json:"lineType"`
	Body       string  `json:"body"`
	Task       bool    `json:"task"`
	Suggestion *string `json:"suggestion"`
	ReplyTo    int     `json:"replyTo"`
}

func (s commentSpec) location() string {
	switch {
	case s.ReplyTo > 0:
		return fmt.Sprintf("reply:%d", s.ReplyTo)
	case s.Path == "":
		return "general"
	case s.Line > 0:
		return fmt.Sprintf("%s:%d", s.Path, s.Line)
	}
	return s.Path
}
