// Command tidydisk finds what is using disk space on Windows and explains
// what is safe to clean. This version only reports; it never deletes.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"tidydisk/internal/catalog"
	"tidydisk/internal/engine"
	"tidydisk/internal/human"
	"tidydisk/internal/report"
	"tidydisk/internal/scan"
)

const usage = `tidydisk - see what is filling your disk and what is safe to clean

Usage:
  tidydisk scan    [flags] [PATH]   biggest folders and files under PATH (default C:\)
  tidydisk report  [flags]          known caches on this PC + big unknown folders (dry run)
  tidydisk compare [flags] [PATH]   run both scan engines on PATH and show the differences

Engines (--engine):
  walk   list folders in parallel through normal Windows calls; no admin needed
  mft    read the NTFS file table directly; much faster, needs an admin terminal
  auto   mft when possible, otherwise walk (default)

Run "tidydisk <command> -h" for the flags of each command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "scan":
		err = cmdScan(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "compare":
		err = cmdCompare(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tidydisk:", err)
		os.Exit(1)
	}
}

// parse lets flags come before or after the path argument.
func parse(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		_ = fs.Parse(args) // ExitOnError handles failures
		if fs.NArg() == 0 {
			return pos
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func absPath(p string) string {
	if len(p) == 2 && p[1] == ':' {
		return p + `\`
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// runEngine scans path and returns the result plus the index of path in it.
func runEngine(name, path string, bigMin int64) (*scan.Result, int32, error) {
	return engine.Run(name, path, engine.Options{
		BigFileMin: bigMin,
		OnFallback: func(err error) { fmt.Fprintf(os.Stderr, "note: %v; using the folder walk instead\n", err) },
	})
}

func pct(part, whole int64) string {
	if whole <= 0 {
		return ""
	}
	return fmt.Sprintf("%.0f%%", float64(part)*100/float64(whole))
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printSummary(w io.Writer, r *scan.Result, i int32) {
	d := r.Dirs[i]
	fmt.Fprintf(w, "%s  (engine: %s, %s)\n", r.Path(i), r.Engine, r.Elapsed.Round(1e6))
	fmt.Fprintf(w, "  %s in %d files and %d folders\n", human.Bytes(d.TotalBytes), d.TotalFiles, d.TotalDirs)
	if r.SkippedLinks > 0 {
		fmt.Fprintf(w, "  %d junctions/symlinks not followed (so nothing is counted twice)\n", r.SkippedLinks)
	}
	if r.Errors > 0 {
		fmt.Fprintf(w, "  %d folders/records could not be read\n", r.Errors)
	}
	if r.Unreachable > 0 {
		fmt.Fprintf(w, "  %s in files with no reachable parent folder\n", human.Bytes(r.Unreachable))
	}
}

func cmdScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	engine := fs.String("engine", "auto", "walk, mft or auto")
	depth := fs.Int("depth", 2, "how many folder levels below PATH to rank")
	top := fs.Int("top", 25, "how many folders and files to list")
	bigFlag := fs.String("big", "256MB", "list single files at least this big")
	asJSON := fs.Bool("json", false, "print JSON instead of a table")
	pos := parse(fs, args)
	path := `C:\`
	if len(pos) > 0 {
		path = pos[0]
	}
	path = absPath(path)
	bigMin, err := human.ParseSize(*bigFlag)
	if err != nil {
		return err
	}

	r, start, err := runEngine(*engine, path, bigMin)
	if err != nil {
		return err
	}
	rootPath := r.Path(start)
	files := report.LargeFiles(r, rootPath, nil, *top)
	topDirs := r.Top(start, *depth, *top)

	if *asJSON {
		type dirOut struct {
			Path  string `json:"path"`
			Bytes int64  `json:"bytes"`
			Files int64  `json:"files"`
		}
		out := struct {
			Engine    string      `json:"engine"`
			Path      string      `json:"path"`
			Bytes     int64       `json:"bytes"`
			ElapsedMS int64       `json:"elapsedMs"`
			Folders   []dirOut    `json:"folders"`
			Files     []scan.File `json:"files"`
		}{Engine: r.Engine, Path: rootPath, Bytes: r.Dirs[start].TotalBytes, ElapsedMS: r.Elapsed.Milliseconds(), Files: files}
		for _, i := range topDirs {
			out.Folders = append(out.Folders, dirOut{r.Path(i), r.Dirs[i].TotalBytes, r.Dirs[i].TotalFiles})
		}
		return writeJSON(out)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	printSummary(os.Stdout, r, start)
	total := r.Dirs[start].TotalBytes
	fmt.Fprintf(os.Stdout, "\nBiggest folders (up to %d levels down):\n", *depth)
	for _, i := range topDirs {
		fmt.Fprintf(w, "%s\t%s\t\t%s\n", human.Bytes(r.Dirs[i].TotalBytes), pct(r.Dirs[i].TotalBytes, total), r.Path(i))
	}
	w.Flush()
	if len(files) > 0 {
		fmt.Fprintf(os.Stdout, "\nFiles of %s or more:\n", human.Bytes(bigMin))
		for _, f := range files {
			fmt.Fprintf(w, "%s\t\t%s\n", human.Bytes(f.Size), f.Path)
		}
		w.Flush()
	}
	return nil
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	engine := fs.String("engine", "auto", "engine for the unknown-folder scan: walk, mft or auto")
	root := fs.String("root", os.Getenv("USERPROFILE"), "where to look for big folders the catalog doesn't know")
	minFlag := fs.String("min", "500MB", "smallest unknown folder or file worth showing")
	top := fs.Int("top", 15, "how many unknown folders and files to list")
	noUnknown := fs.Bool("no-unknown", false, "skip the unknown-folder scan")
	noResolve := fs.Bool("no-resolve", false, "don't ask npm, pip, go... where their caches are")
	asJSON := fs.Bool("json", false, "print JSON instead of a table")
	parse(fs, args)
	minBytes, err := human.ParseSize(*minFlag)
	if err != nil {
		return err
	}

	entries, err := catalog.Builtin()
	if err != nil {
		return err
	}
	findings := report.Collect(entries, report.Options{RunResolvers: !*noResolve})

	var hotspots []report.Hotspot
	var files []scan.File
	if !*noUnknown {
		rootPath := absPath(*root)
		r, start, err := runEngine(*engine, rootPath, minBytes)
		if err != nil {
			return err
		}
		var covered []string
		for _, f := range findings {
			covered = append(covered, f.Path)
		}
		hotspots = report.Hotspots(r, start, covered, minBytes, *top)
		files = report.LargeFiles(r, r.Path(start), covered, *top)
	}

	if *asJSON {
		return writeJSON(struct {
			Findings []report.Finding `json:"findings"`
			Unknown  []report.Hotspot `json:"unknownFolders"`
			Files    []scan.File      `json:"largeFiles"`
		}{findings, hotspots, files})
	}

	groups := []struct{ risk, title string }{
		{catalog.RiskSafe, "SAFE TO CLEAN (rebuilt automatically)"},
		{catalog.RiskReview, "REVIEW FIRST (probably fine, your call)"},
		{catalog.RiskSystem, "WINDOWS-MANAGED (admin, use Windows' own tools)"},
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, g := range groups {
		var list []report.Finding
		var sum int64
		for _, f := range findings {
			if f.Risk == g.risk {
				list = append(list, f)
				sum += f.Bytes
			}
		}
		if len(list) == 0 {
			continue
		}
		sort.SliceStable(list, func(a, b int) bool { return list[a].Bytes > list[b].Bytes })
		fmt.Fprintf(os.Stdout, "\n%s  -  %s total\n", g.title, human.Bytes(sum))
		for _, f := range list {
			size := human.Bytes(f.Bytes)
			if !f.Measured {
				size = "not measured"
			}
			extra := []string{f.Clean}
			if len(f.CloseFirst) > 0 {
				extra = append(extra, "close "+strings.Join(f.CloseFirst, ", ")+" first")
			}
			if f.Admin {
				extra = append(extra, "admin")
			}
			if f.Status != "" {
				extra = append(extra, f.Status)
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", size, f.App, f.Path, strings.Join(extra, " | "))
		}
		w.Flush()
	}
	if len(hotspots) > 0 {
		fmt.Fprintf(os.Stdout, "\nBIG FOLDERS THE CATALOG DOESN'T KNOW (look before deleting)\n")
		for _, h := range hotspots {
			fmt.Fprintf(w, "  %s\t%s\n", human.Bytes(h.Bytes), h.Path)
		}
		w.Flush()
	}
	if len(files) > 0 {
		fmt.Fprintf(os.Stdout, "\nBIG FILES THE CATALOG DOESN'T KNOW\n")
		for _, f := range files {
			fmt.Fprintf(w, "  %s\t%s\n", human.Bytes(f.Size), f.Path)
		}
		w.Flush()
	}
	fmt.Println("\nDry run: nothing was deleted.")
	return nil
}

func cmdCompare(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	top := fs.Int("top", 20, "how many subfolders to compare")
	pos := parse(fs, args)
	path := `C:\`
	if len(pos) > 0 {
		path = pos[0]
	}
	path = absPath(path)

	wr, _, err := runEngine("walk", path, 0)
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}
	mr, mi, err := runEngine("mft", path, 0)
	if err != nil {
		return fmt.Errorf("mft: %w", err)
	}

	printSummary(os.Stdout, wr, 0)
	printSummary(os.Stdout, mr, mi)
	fmt.Println("  (the mft time covers reading the whole volume)")

	type row struct {
		name      string
		walk, mft int64
	}
	rows := map[string]*row{}
	for _, c := range wr.Children(0) {
		k := strings.ToLower(wr.Dirs[c].Name)
		rows[k] = &row{name: wr.Dirs[c].Name, walk: wr.Dirs[c].TotalBytes}
	}
	for _, c := range mr.Children(mi) {
		k := strings.ToLower(mr.Dirs[c].Name)
		if rows[k] == nil {
			rows[k] = &row{name: mr.Dirs[c].Name}
		}
		rows[k].mft = mr.Dirs[c].TotalBytes
	}
	list := make([]*row, 0, len(rows))
	for _, r := range rows {
		list = append(list, r)
	}
	sort.Slice(list, func(a, b int) bool { return max(list[a].walk, list[a].mft) > max(list[b].walk, list[b].mft) })
	if len(list) > *top {
		list = list[:*top]
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "\nwalk\tmft\tdifference\t\tfolder")
	for _, r := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t\t%s\n", human.Bytes(r.walk), human.Bytes(r.mft), human.Bytes(r.mft-r.walk), r.name)
	}
	fmt.Fprintf(w, "%s\t%s\t%s\t\t(files directly in %s)\n", human.Bytes(wr.Dirs[0].FileBytes), human.Bytes(mr.Dirs[mi].FileBytes),
		human.Bytes(mr.Dirs[mi].FileBytes-wr.Dirs[0].FileBytes), path)
	w.Flush()
	fmt.Println(`
Why the numbers differ:
  - walk counts every hard link (WinSxS and System32 share files); mft counts each file once
  - walk can't open folders it has no permission for; mft sees everything
  - mft counts sparse/compressed files at their on-disk size, walk at their logical size
  - at the drive root, mft also counts NTFS metadata files such as $MFT`)
	return nil
}
