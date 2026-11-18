//go:build windows

package scan

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalkSkipsJunctions(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a", "x.bin"), 100)
	write(t, filepath.Join(root, "a", "b", "y.bin"), 50)
	write(t, filepath.Join(root, "top.bin"), 7)
	// A junction back to "a" would double the size if it were followed.
	out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(root, "link"), filepath.Join(root, "a")).CombinedOutput()
	if err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}

	r, err := Walk(root, Options{Workers: 2, BigFileMin: 60})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Dirs[0].TotalBytes; got != 157 {
		t.Fatalf("total = %d, want 157", got)
	}
	if r.SkippedLinks != 1 {
		t.Fatalf("skipped links = %d", r.SkippedLinks)
	}
	if len(r.Dirs) != 3 || r.Dirs[0].TotalDirs != 2 || r.Dirs[0].TotalFiles != 3 {
		t.Fatalf("tree = %+v", r.Dirs)
	}
	b, ok := r.Find(filepath.Join(root, "A", "B"))
	if !ok || r.Dirs[b].TotalBytes != 50 || r.Path(b) != filepath.Join(root, "a", "b") {
		t.Fatalf("find a\\b: ok=%v idx=%d", ok, b)
	}
	if len(r.BigFiles) != 1 || r.BigFiles[0].Size != 100 {
		t.Fatalf("big files = %+v", r.BigFiles)
	}
	top := r.Top(0, 2, 10)
	if len(top) != 2 || r.Dirs[top[0]].Name != "a" {
		t.Fatalf("top = %v", top)
	}
}
