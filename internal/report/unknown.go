package report

import (
	"sort"
	"strings"

	"tidydisk/internal/scan"
)

// Hotspot is a folder the catalog doesn't cover where a lot of space sits.
type Hotspot struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"` // bytes in this folder not already covered by a finding
}

// Hotspots finds where uncatalogued space concentrates below start.
//
// A folder is reported when at least minBytes of uncatalogued data sits under
// it and no single subfolder holds half or more of that, otherwise the
// subfolder is the better answer. This points at "WWE 2K22" rather than at
// "AppData" or "Local".
func Hotspots(r *scan.Result, start int32, covered []string, minBytes int64, limit int) []Hotspot {
	n := len(r.Dirs)
	inSub := make([]bool, n)
	matched := make([]bool, n)
	inMatch := make([]bool, n)
	for _, p := range covered {
		if i, ok := r.Find(p); ok {
			matched[i] = true
		}
	}
	for i := 0; i < n; i++ { // parents come before children
		p := r.Dirs[i].Parent
		inSub[i] = int32(i) == start || (p >= 0 && inSub[p])
		inMatch[i] = matched[i] || (p >= 0 && inMatch[p])
	}

	// coveredBytes[i] = bytes under i that belong to a covered folder.
	coveredBytes := make([]int64, n)
	for i := n - 1; i >= 0; i-- {
		if matched[i] && (r.Dirs[i].Parent < 0 || !inMatch[r.Dirs[i].Parent]) {
			coveredBytes[i] = r.Dirs[i].TotalBytes
		}
		if p := r.Dirs[i].Parent; i > 0 && !inMatch[p] {
			coveredBytes[p] += coveredBytes[i]
		}
	}
	unknown := func(i int32) int64 { return r.Dirs[i].TotalBytes - coveredBytes[i] }

	var out []Hotspot
	underReported := make([]bool, n) // an ancestor is already listed
	for i := int32(0); i < int32(n); i++ {
		if p := r.Dirs[i].Parent; p >= 0 && underReported[p] {
			underReported[i] = true
			continue
		}
		if !inSub[i] || inMatch[i] {
			continue
		}
		u := unknown(i)
		if u < minBytes {
			continue
		}
		dominated := false
		for _, c := range r.Children(i) {
			if !inMatch[c] && unknown(c)*2 >= u {
				dominated = true
				break
			}
		}
		if !dominated {
			out = append(out, Hotspot{Path: r.Path(i), Bytes: u})
			underReported[i] = true
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Bytes > out[b].Bytes })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// LargeFiles lists big files under under that no finding already covers.
func LargeFiles(r *scan.Result, under string, covered []string, limit int) []scan.File {
	prefix := strings.ToLower(strings.TrimRight(under, `\`)) + `\`
	var out []scan.File
	for _, f := range r.BigFiles {
		lp := strings.ToLower(f.Path)
		if !strings.HasPrefix(lp, prefix) {
			continue
		}
		skip := false
		for _, c := range covered {
			if related(f.Path, c) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, f)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
