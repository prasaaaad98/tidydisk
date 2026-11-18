package catalog

import (
	"os"
	"strings"
	"testing"
)

func TestBuiltinCatalogIsValid(t *testing.T) {
	entries, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 20 {
		t.Fatalf("only %d entries", len(entries))
	}
	for _, e := range entries {
		if e.Risk == RiskSystem && !e.Admin {
			t.Errorf("%s: Windows-managed entries should be marked admin", e.ID)
		}
		for _, p := range e.Paths {
			if strings.Contains(strings.ToLower(p), `c:\users\`) {
				t.Errorf("%s: hard-coded user path %q; use %%USERPROFILE%%", e.ID, p)
			}
		}
	}
}

func TestParseRejectsBadEntries(t *testing.T) {
	bad := []string{
		`{"version":1,"entries":[{"id":"x","app":"a","risk":"maybe","paths":["p"],"clean":{"manual":"m"}}]}`,
		`{"version":1,"entries":[{"id":"x","app":"a","risk":"safe","paths":["p"],"clean":{}}]}`,
		`{"version":1,"entries":[{"id":"x","app":"a","risk":"safe","paths":["p"],"clean":{"manual":"m"},"typo":1}]}`,
	}
	for _, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("accepted %s", doc)
		}
	}
}

func TestExpand(t *testing.T) {
	os.Setenv("TIDYDISK_TEST", `D:\x`)
	if got := Expand(`%TIDYDISK_TEST%\cache`); got != `D:\x\cache` {
		t.Fatalf("got %q", got)
	}
	if got := Expand(`%NOPE_NOT_SET%\a`); got != `%NOPE_NOT_SET%\a` {
		t.Fatalf("got %q", got)
	}
}
