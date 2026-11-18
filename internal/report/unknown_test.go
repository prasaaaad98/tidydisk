package report

import (
	"testing"

	"tidydisk/internal/scan"
)

const gb = int64(1) << 30

// tree:  C:\Users\me
//
//	AppData\Local\Game        8 GB  (one file)       -> unknown hotspot
//	AppData\Local\npm-cache   2 GB                   -> covered by a finding
//	AppData\Local\Many\a,b,c  1 GB each              -> hotspot is "Many"
//	Docs                      0.1 GB                 -> too small
func testTree() *scan.Result {
	r := &scan.Result{Root: `C:\Users\me`}
	add := func(name string, parent int32, bytes int64) int32 {
		r.Dirs = append(r.Dirs, scan.Dir{Name: name, Parent: parent, FileBytes: bytes})
		return int32(len(r.Dirs) - 1)
	}
	root := add(`C:\Users\me`, -1, 0)
	app := add("AppData", root, 0)
	local := add("Local", app, 0)
	add("Game", local, 8*gb)
	add("npm-cache", local, 2*gb)
	many := add("Many", local, 0)
	add("a", many, gb)
	add("b", many, gb)
	add("c", many, gb)
	add("Docs", root, gb/10)
	r.BigFiles = []scan.File{
		{Path: `C:\Users\me\AppData\Local\Game\PreBakeCache.bin`, Size: 8 * gb},
		{Path: `C:\Users\me\AppData\Local\npm-cache\huge.tgz`, Size: gb},
	}
	r.Finalize()
	return r
}

func TestHotspots(t *testing.T) {
	r := testTree()
	covered := []string{`C:\Users\me\AppData\Local\npm-cache`}
	got := Hotspots(r, 0, covered, gb/2, 10)
	want := []Hotspot{
		{Path: `C:\Users\me\AppData\Local\Game`, Bytes: 8 * gb},
		{Path: `C:\Users\me\AppData\Local\Many`, Bytes: 3 * gb},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hotspot %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestLargeFilesSkipsCovered(t *testing.T) {
	r := testTree()
	files := LargeFiles(r, `C:\Users\me`, []string{`C:\Users\me\AppData\Local\npm-cache`}, 10)
	if len(files) != 1 || files[0].Size != 8*gb {
		t.Fatalf("got %+v", files)
	}
}

func TestRelated(t *testing.T) {
	if !related(`C:\A\npm-cache`, `c:\a\NPM-CACHE\_cacache`) || related(`C:\A\npm`, `C:\A\npm-cache`) {
		t.Fatal("related() is wrong")
	}
}

func TestIsUnder(t *testing.T) {
	cases := []struct {
		p, root string
		want    bool
	}{
		{`C:\Users\me`, `C:\`, true},
		{`c:\users`, `C:\Users`, true},
		{`D:\npm-cache`, `C:\`, false},
		{`C:\Users2`, `C:\Users`, false},
	}
	for _, c := range cases {
		if got := IsUnder(c.p, c.root); got != c.want {
			t.Errorf("IsUnder(%q, %q) = %v", c.p, c.root, got)
		}
	}
}
