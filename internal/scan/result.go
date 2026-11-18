// Package scan holds the folder-size tree that every scan engine produces,
// plus the ordinary parallel folder walker.
package scan

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dir is one folder in the result tree. Engines must append a folder after
// its parent, so Parent is always lower than the folder's own index.
type Dir struct {
	Name       string
	Parent     int32 // -1 for the root
	Depth      int32
	FileCount  int64 // files directly inside this folder
	FileBytes  int64
	TotalBytes int64 // this folder and everything below it (set by Finalize)
	TotalFiles int64
	TotalDirs  int64
}

// File is a single large file worth showing on its own.
type File struct {
	Path string
	Size int64
}

// Result is what a scan engine returns.
type Result struct {
	Engine         string
	Root           string
	Dirs           []Dir
	BigFiles       []File
	SkippedLinks   int64    // junctions/symlinks not followed (walk engine)
	SkippedSamples []string // a few examples of SkippedLinks
	Errors         int64    // folders or records that could not be read
	ErrorSamples   []string
	Unreachable    int64 // bytes found but not attached to the tree (mft engine)
	Elapsed        time.Duration

	children [][]int32
}

const maxSamples = 20

// Finalize rolls file sizes up into folder totals and sorts the big files.
func (r *Result) Finalize() {
	for i := range r.Dirs {
		d := &r.Dirs[i]
		d.TotalBytes, d.TotalFiles, d.TotalDirs = d.FileBytes, d.FileCount, 0
	}
	for i := len(r.Dirs) - 1; i > 0; i-- {
		d := r.Dirs[i]
		p := &r.Dirs[d.Parent]
		p.TotalBytes += d.TotalBytes
		p.TotalFiles += d.TotalFiles
		p.TotalDirs += d.TotalDirs + 1
	}
	sort.Slice(r.BigFiles, func(a, b int) bool { return r.BigFiles[a].Size > r.BigFiles[b].Size })
	r.children = nil
}

// JoinPath joins Windows path parts without doubling the separator.
func JoinPath(a, b string) string {
	if strings.HasSuffix(a, `\`) {
		return a + b
	}
	return a + `\` + b
}

// Path returns the full path of folder i.
func (r *Result) Path(i int32) string {
	var parts []string
	for ; i > 0; i = r.Dirs[i].Parent {
		parts = append(parts, r.Dirs[i].Name)
	}
	p := r.Root
	for j := len(parts) - 1; j >= 0; j-- {
		p = JoinPath(p, parts[j])
	}
	return p
}

// Children returns the folders directly inside folder i.
func (r *Result) Children(i int32) []int32 {
	if r.children == nil {
		c := make([][]int32, len(r.Dirs))
		for j := 1; j < len(r.Dirs); j++ {
			p := r.Dirs[j].Parent
			c[p] = append(c[p], int32(j))
		}
		r.children = c
	}
	return r.children[i]
}

// Find looks up a folder by path, ignoring case.
func (r *Result) Find(path string) (int32, bool) {
	root := strings.TrimRight(r.Root, `\`)
	p := strings.TrimRight(filepath.Clean(path), `\`)
	if len(p) < len(root) || !strings.EqualFold(p[:len(root)], root) {
		return -1, false
	}
	if len(p) > len(root) && p[len(root)] != '\\' {
		return -1, false
	}
	rest := strings.Trim(p[len(root):], `\`)
	cur := int32(0)
	if rest == "" {
		return 0, true
	}
	for _, part := range strings.Split(rest, `\`) {
		found := false
		for _, c := range r.Children(cur) {
			if strings.EqualFold(r.Dirs[c].Name, part) {
				cur, found = c, true
				break
			}
		}
		if !found {
			return -1, false
		}
	}
	return cur, true
}

// Top returns up to n folders between 1 and maxDepth levels below start,
// largest first.
func (r *Result) Top(start int32, maxDepth, n int) []int32 {
	var out []int32
	var visit func(i int32, depth int)
	visit = func(i int32, depth int) {
		if depth > maxDepth {
			return
		}
		for _, c := range r.Children(i) {
			out = append(out, c)
			visit(c, depth+1)
		}
	}
	visit(start, 1)
	sort.Slice(out, func(a, b int) bool { return r.Dirs[out[a]].TotalBytes > r.Dirs[out[b]].TotalBytes })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func addSample(list *[]string, s string) {
	if len(*list) < maxSamples {
		*list = append(*list, s)
	}
}
