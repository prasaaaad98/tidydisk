// Package report matches the catalog against this PC and finds big folders the
// catalog doesn't know about. It never deletes anything.
package report

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tidydisk/internal/catalog"
	"tidydisk/internal/scan"
)

// Finding is one catalog match on this PC.
type Finding struct {
	ID         string   `json:"id"`
	App        string   `json:"app"`
	Category   string   `json:"category"`
	Risk       string   `json:"risk"`
	Path       string   `json:"path"`
	Bytes      int64    `json:"bytes"`
	Measured   bool     `json:"measured"`
	Status     string   `json:"status,omitempty"`
	Clean      string   `json:"clean"`
	CloseFirst []string `json:"closeFirst,omitempty"`
	Admin      bool     `json:"admin,omitempty"`
	Rebuilds   string   `json:"rebuilds,omitempty"`
	Notes      string   `json:"notes,omitempty"`
}

// Options for Collect.
type Options struct {
	RunResolvers bool // ask tools (npm, pip, go...) where their cache really is
}

// Collect finds and measures every catalog entry present on this PC.
func Collect(entries []catalog.Entry, opt Options) []Finding {
	var out []Finding
	for _, e := range entries {
		active := ""
		if opt.RunResolvers {
			active = resolveActive(e)
		}
		for _, p := range candidatePaths(e, active) {
			if e.Detector == "vscode-obsolete" {
				out = append(out, obsoleteExtensions(e, p)...)
				continue
			}
			f := newFinding(e, p)
			if active != "" {
				if related(p, active) {
					f.Status = "in use"
				} else {
					f.Status = "leftover: " + e.App + " now uses " + active
				}
			}
			if !e.SkipMeasure {
				var err error
				f.Bytes, err = measure(p)
				f.Measured = err == nil
				if err != nil {
					f.Status = joinStatus(f.Status, "could not read (try an admin terminal)")
				}
			}
			out = append(out, f)
		}
	}
	return out
}

func newFinding(e catalog.Entry, p string) Finding {
	return Finding{
		ID: e.ID, App: e.App, Category: e.Category, Risk: e.Risk, Path: p,
		Clean: e.Clean.Describe(), CloseFirst: e.CloseFirst, Admin: e.Admin,
		Rebuilds: e.Rebuilds, Notes: e.Notes,
	}
}

func joinStatus(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func candidatePaths(e catalog.Entry, active string) []string {
	paths := e.ExistingPaths()
	if active != "" {
		if _, err := os.Stat(active); err == nil {
			paths = append(paths, active)
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		k := strings.ToLower(filepath.Clean(p))
		if !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}

// resolveActive asks the tool itself where its cache is. Tools often have
// their cache moved (npm's here was on D:), which leaves the old folder behind.
func resolveActive(e catalog.Entry) string {
	if e.ResolveEnv != "" {
		if v := os.Getenv(e.ResolveEnv); v != "" {
			return filepath.Clean(v)
		}
	}
	if len(e.Resolve) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, e.Resolve[0], e.Resolve[1:]...).Output()
	if err != nil {
		return "" // tool not installed or failed
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if !filepath.IsAbs(line) {
		return ""
	}
	return filepath.Clean(line)
}

func related(a, b string) bool {
	a, b = strings.ToLower(filepath.Clean(a)), strings.ToLower(filepath.Clean(b))
	return a == b || strings.HasPrefix(a, b+`\`) || strings.HasPrefix(b, a+`\`)
}

var errNoAccess = errors.New("no access")

func measure(p string) (int64, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return 0, err
	}
	if !st.IsDir() {
		return st.Size(), nil
	}
	r, err := scan.Walk(p, scan.Options{})
	if err != nil {
		return 0, err
	}
	if r.Errors > 0 && r.Dirs[0].TotalBytes == 0 {
		return 0, errNoAccess
	}
	return r.Dirs[0].TotalBytes, nil
}

// obsoleteExtensions reads the .obsolete file VS Code (and its forks) keep
// in the extensions folder: versions listed there are waiting to be removed.
func obsoleteExtensions(e catalog.Entry, dir string) []Finding {
	b, err := os.ReadFile(filepath.Join(dir, ".obsolete"))
	if err != nil {
		return nil
	}
	var m map[string]bool
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	names := make([]string, 0, len(m))
	for name, obsolete := range m {
		if obsolete {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var out []Finding
	for _, name := range names {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		f := newFinding(e, p)
		f.Status = "old version, marked for removal by the editor"
		f.Bytes, err = measure(p)
		f.Measured = err == nil
		out = append(out, f)
	}
	return out
}
