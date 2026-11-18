// Package catalog is the list of known space users: where they live, how
// risky they are to clear, and how to clear them properly.
package catalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed catalog.json
var builtin []byte

// Risk levels.
const (
	RiskSafe   = "safe"   // rebuilt automatically; nothing personal inside
	RiskReview = "review" // probably fine, but the user should decide
	RiskSystem = "system" // Windows-managed; only clean with Windows' own tools
)

// Clean says how an entry should be cleaned. Exactly one field is set.
type Clean struct {
	Command        []string `json:"command,omitempty"`        // the tool's own cleanup command (preferred)
	DeleteContents bool     `json:"deleteContents,omitempty"` // empty the folder, keep the folder
	Manual         string   `json:"manual,omitempty"`         // instructions for the user
}

// Describe renders the clean method as one line.
func (c Clean) Describe() string {
	switch {
	case len(c.Command) > 0:
		return "run: " + strings.Join(c.Command, " ")
	case c.DeleteContents:
		return "delete folder contents"
	default:
		return c.Manual
	}
}

// Entry is one known space user.
type Entry struct {
	ID          string   `json:"id"`
	Group       string   `json:"group"` // who cares about it: dev, games, browsers, windows, other
	App         string   `json:"app"`
	Category    string   `json:"category"`
	Risk        string   `json:"risk"`
	Paths       []string `json:"paths"`                // %ENV% variables and * globs allowed
	Resolve     []string `json:"resolve,omitempty"`    // command that prints the location in use
	ResolveEnv  string   `json:"resolveEnv,omitempty"` // env var that moves the location
	Detector    string   `json:"detector,omitempty"`   // special finder, e.g. "vscode-obsolete"
	Clean       Clean    `json:"clean"`
	CloseFirst  []string `json:"closeFirst,omitempty"`  // processes that must not be running
	Admin       bool     `json:"admin,omitempty"`       // needs an administrator terminal
	SkipMeasure bool     `json:"skipMeasure,omitempty"` // measuring is slow or misleading
	Rebuilds    string   `json:"rebuilds,omitempty"`
	Notes       string   `json:"notes,omitempty"`
}

var detectors = map[string]bool{"vscode-obsolete": true}

// Groups are the audiences an entry belongs to, used for filtering.
var Groups = []string{"dev", "games", "browsers", "windows", "other"}

func validGroup(g string) bool {
	for _, x := range Groups {
		if g == x {
			return true
		}
	}
	return false
}

// Validate checks one entry.
func (e Entry) Validate() error {
	if e.ID == "" || e.App == "" {
		return fmt.Errorf("entry %q: id and app are required", e.ID)
	}
	if !validGroup(e.Group) {
		return fmt.Errorf("entry %q: group must be one of %v", e.ID, Groups)
	}
	if e.Risk != RiskSafe && e.Risk != RiskReview && e.Risk != RiskSystem {
		return fmt.Errorf("entry %q: risk must be safe, review or system", e.ID)
	}
	if len(e.Paths) == 0 {
		return fmt.Errorf("entry %q: no paths", e.ID)
	}
	if e.Detector != "" && !detectors[e.Detector] {
		return fmt.Errorf("entry %q: unknown detector %q", e.ID, e.Detector)
	}
	n := 0
	if len(e.Clean.Command) > 0 {
		n++
	}
	if e.Clean.DeleteContents {
		n++
	}
	if e.Clean.Manual != "" {
		n++
	}
	if n != 1 {
		return fmt.Errorf("entry %q: clean must set exactly one of command, deleteContents, manual", e.ID)
	}
	return nil
}

// Builtin returns the catalog compiled into the program.
func Builtin() ([]Entry, error) { return Parse(builtin) }

// Parse reads and validates a catalog document.
func Parse(b []byte) ([]Entry, error) {
	var doc struct {
		Version int     `json:"version"`
		Entries []Entry `json:"entries"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	seen := map[string]bool{}
	for _, e := range doc.Entries {
		if err := e.Validate(); err != nil {
			return nil, err
		}
		if seen[e.ID] {
			return nil, fmt.Errorf("catalog: duplicate id %q", e.ID)
		}
		seen[e.ID] = true
	}
	return doc.Entries, nil
}

var envRef = regexp.MustCompile(`%([^%\\]+)%`)

// Expand replaces %VAR% references. Unknown variables are left as they are,
// so the path simply won't exist.
func Expand(p string) string {
	return envRef.ReplaceAllStringFunc(p, func(m string) string {
		if v, ok := os.LookupEnv(m[1 : len(m)-1]); ok {
			return v
		}
		return m
	})
}

// ExistingPaths expands an entry's paths and globs and returns those that exist.
func (e Entry) ExistingPaths() []string {
	var out []string
	for _, raw := range e.Paths {
		p := Expand(raw)
		if strings.ContainsAny(p, "*?[") {
			matches, _ := filepath.Glob(p)
			out = append(out, matches...)
			continue
		}
		if _, err := os.Lstat(p); err == nil {
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}
