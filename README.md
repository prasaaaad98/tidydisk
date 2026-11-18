# tidydisk

See what is filling a Windows drive and which parts are safe to clean.
This version **only reports**. It never deletes anything.

## Desktop app

`tidydisk-gui.exe` is a small window over the same engine: pick a drive, press **Analyze**, filter
by audience (Dev, Games, Browsers, Windows), and expand a row to see how to clean it, what to close
first and what happens afterwards. It shows the folder in Explorer and copies the cleanup command.
It offers to restart as administrator to use the fast file table scan.

It uses the WebView2 runtime that ships with Windows 11 (through the pure-Go
`github.com/jchv/go-webview2`), so there is no C compiler, Node or Electron involved. The whole UI is
one file, `cmd/tidydisk-gui/ui.html`. Opened in a normal browser, it runs on sample data for design work.

```
go build -ldflags "-H windowsgui -s -w" -o tidydisk-gui.exe ./cmd/tidydisk-gui
```

Set `TIDYDISK_DEBUG=1` to enable right-click and DevTools in the window.

## Command line

```
tidydisk scan    [PATH]   biggest folders and files (default C:\)
tidydisk report           known caches on this PC + big unknown folders (dry run)
tidydisk compare [PATH]   run both scan engines and show where they differ
```

Add `--json` to `scan` or `report` for machine-readable output.

## Two scan engines

| | `walk` (folder walk) | `mft` (file table) |
|---|---|---|
| How | Lists folders in parallel through normal Windows calls | Reads the NTFS Master File Table straight off the volume |
| Admin needed | No | Yes (opens `\\.\C:`) |
| Speed | ~20 s for a 100 GB drive | Usually a few seconds; one sequential read |
| Hard links | Counted once per link (WinSxS looks bigger than it is) | Counted once |
| Locked folders | Skipped and counted as errors | Visible |
| Drives | Any file system | NTFS only |

`--engine auto` (the default) tries `mft` and falls back to `walk`.

**Walk safety:** junctions and symlinks are never followed, so nothing is
counted twice and the walk can't leave the folder you asked for. OneDrive
placeholder folders are real folders and are followed.

**How the file table scan works** (`internal/mft`):
1. Read the boot sector to get the cluster size, record size and where the table starts.
2. Read record 0 (the table's own entry). Its data runs list where the rest of the table is on disk.
3. Read the table in 4 MB chunks. Each 1 KB record gets its fixup applied (NTFS's torn-write
   protection), then we read the file's name and parent folder (`$FILE_NAME`) and its size (`$DATA`).
4. Extension records add their attributes to their base record. Sparse and compressed
   files count at their on-disk size.
5. Link records into a tree from the root folder (record 5) and add up the sizes.

Known gaps: the table's own record list must fit in record 0 (it warns if it doesn't),
and parsing is single-threaded.

## The catalog

`internal/catalog/catalog.json` is where most of the value lives. Each entry says:

- `paths`: where the data lives. `%ENV%` variables and `*` globs are allowed.
- `risk`: `safe` (rebuilds itself), `review` (the user decides), `system` (Windows-managed, admin).
- `clean`: the tool's own command (preferred), `deleteContents`, or `manual` instructions.
- `resolve` / `resolveEnv`: ask the tool where its cache **really** is. Anything at the
  default location that isn't the active one is reported as a leftover. (On the first PC
  this ran on, npm and Go had moved their caches to D:, and the old copies on C: were still there.)
- `closeFirst`, `admin`, `rebuilds`, `notes`: what the user needs to know first.
- `detector: "vscode-obsolete"`: reads VS Code's `.obsolete` file to find old extension versions.

Things the catalog doesn't know about are still shown as "big folders/files the catalog
doesn't know". A folder is listed when no single subfolder holds half its size, so the
list points at the folder that matters (`...\WWE 2K22`), not at `AppData`.

## Build and test

```
go test ./...
go build -o tidydisk.exe ./cmd/tidydisk
```

Go 1.23+, Windows only. The command-line tool uses only the standard library and `golang.org/x/sys`;
the desktop app adds `github.com/jchv/go-webview2`.

## Next steps

- `clean` command: Recycle Bin by default, permanent only when asked, skip locked and
  recently changed files, refuse while `closeFirst` processes run, write a log.
- Run the tool's own `clean.command` instead of deleting files where one exists.
- Sign releases (SignPath offers free signing for open-source projects) to avoid SmartScreen warnings.
