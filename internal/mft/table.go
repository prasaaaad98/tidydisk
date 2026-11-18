package mft

import (
	"math"

	"tidydisk/internal/scan"
)

const rootRecord = 5 // NTFS always keeps the root folder in record 5

const (
	fInUse uint8 = 1 << iota
	fDir
	fBase
	fNamed
)

type entry struct {
	parent uint32
	flags  uint8
	ns     uint8
	size   int64
	name   string
}

// table collects every file record, then turns them into a scan.Result.
type table struct {
	e   []entry
	bad int64
}

func newTable(n int64) *table { return &table{e: make([]entry, n)} }

// add parses one raw record (fixups are applied in place).
func (t *table) add(recNo int64, raw []byte) {
	if string(raw[0:4]) != "FILE" {
		return // never used, or zeroed
	}
	if err := applyFixup(raw); err != nil {
		t.bad++
		return
	}
	r, err := parseRecord(raw, false)
	if err != nil {
		t.bad++
		return
	}
	if !r.inUse {
		return
	}
	target := recNo
	if r.base != 0 {
		// Extension record: its attributes belong to the base record.
		target = int64(r.base)
		if target >= int64(len(t.e)) {
			t.bad++
			return
		}
	}
	e := &t.e[target]
	if r.base == 0 {
		e.flags |= fInUse | fBase
		if r.isDir {
			e.flags |= fDir
		}
	}
	e.size += r.dataBytes
	if n, ok := bestName(r.names); ok && n.parent <= math.MaxUint32 {
		if e.flags&fNamed == 0 || (e.ns == nsDOS && n.namespace != nsDOS) {
			e.name, e.parent, e.ns = n.name, uint32(n.parent), n.namespace
			e.flags |= fNamed
		}
	}
}

func (t *table) isDir(i int) bool {
	f := t.e[i].flags
	return f&(fInUse|fBase|fDir) == fInUse|fBase|fDir
}

// build links records into a folder tree rooted at record 5.
func (t *table) build(root string, bigMin int64) *scan.Result {
	n := len(t.e)
	res := &scan.Result{Engine: "mft", Root: root, Errors: t.bad}
	if n <= rootRecord || !t.isDir(rootRecord) {
		res.Errors++
		res.ErrorSamples = append(res.ErrorSamples, "root folder record is missing")
		res.Dirs = []scan.Dir{{Name: root, Parent: -1}}
		res.Finalize()
		return res
	}

	// Linked lists of child folders per parent record.
	head := make([]int32, n)
	next := make([]int32, n)
	for i := range head {
		head[i] = -1
	}
	for i := 0; i < n; i++ {
		if i == rootRecord || !t.isDir(i) || t.e[i].flags&fNamed == 0 {
			continue
		}
		p := int(t.e[i].parent)
		if p >= n || !t.isDir(p) {
			continue
		}
		next[i] = head[p]
		head[p] = int32(i)
	}

	// Breadth-first, so every folder lands after its parent.
	dirIdx := make([]int32, n)
	for i := range dirIdx {
		dirIdx[i] = -1
	}
	dirIdx[rootRecord] = 0
	res.Dirs = append(res.Dirs, scan.Dir{Name: root, Parent: -1})
	queue := []int32{rootRecord}
	for qi := 0; qi < len(queue); qi++ {
		rec := queue[qi]
		pi := dirIdx[rec]
		for c := head[rec]; c != -1; c = next[c] {
			if dirIdx[c] != -1 {
				continue
			}
			dirIdx[c] = int32(len(res.Dirs))
			res.Dirs = append(res.Dirs, scan.Dir{Name: t.e[c].name, Parent: pi, Depth: res.Dirs[pi].Depth + 1})
			queue = append(queue, c)
		}
	}

	for i := 0; i < n; i++ {
		e := &t.e[i]
		if e.flags&(fInUse|fBase) != fInUse|fBase || e.flags&fDir != 0 {
			continue
		}
		p := int(e.parent)
		if e.flags&fNamed == 0 || p >= n || dirIdx[p] < 0 {
			res.Unreachable += e.size
			continue
		}
		d := &res.Dirs[dirIdx[p]]
		d.FileCount++
		d.FileBytes += e.size
		if bigMin > 0 && e.size >= bigMin {
			res.BigFiles = append(res.BigFiles, scan.File{Path: scan.JoinPath(res.Path(dirIdx[p]), e.name), Size: e.size})
		}
	}
	res.Finalize()
	return res
}
