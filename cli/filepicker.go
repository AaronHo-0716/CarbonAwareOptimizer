package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// selectableExts is the set of file extensions the user may select.
var selectableExts = map[string]bool{
	".json": true,
	".tf":   true,
}

// ── File entry ────────────────────────────────────────────────────────────────

type fileEntry struct {
	name  string
	info  os.FileInfo
	isDot bool // true for the synthetic "." and ".." rows
}

// ── Custom file picker ────────────────────────────────────────────────────────

type customFilePicker struct {
	currentDir string
	entries    []fileEntry
	cursor     int
	height     int // number of rows visible at once
	offset     int // first visible row index
}

func newFilePicker(dir string) customFilePicker {
	fp := customFilePicker{currentDir: dir, height: 20}
	fp.loadDir()
	return fp
}

// loadDir refreshes the listing for the current directory.
func (fp *customFilePicker) loadDir() {
	var entries []fileEntry

	// Synthetic "." row
	if info, err := os.Stat(fp.currentDir); err == nil {
		entries = append(entries, fileEntry{name: ".", info: info, isDot: true})
	}
	// Synthetic ".." row (skip at filesystem root)
	parent := filepath.Dir(fp.currentDir)
	if parent != fp.currentDir {
		if info, err := os.Stat(parent); err == nil {
			entries = append(entries, fileEntry{name: "..", info: info, isDot: true})
		}
	}

	dirEntries, err := os.ReadDir(fp.currentDir)
	if err != nil {
		fp.entries = entries
		fp.cursor = 0
		fp.offset = 0
		return
	}

	var dirs, files []fileEntry
	for _, de := range dirEntries {
		full := filepath.Join(fp.currentDir, de.Name())
		info, err := os.Stat(full) // follow symlinks
		if err != nil {
			info, err = os.Lstat(full) // fallback for broken symlinks
			if err != nil {
				continue
			}
		}
		e := fileEntry{name: de.Name(), info: info}
		if info.IsDir() {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].name < dirs[j].name })
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	entries = append(entries, dirs...)
	entries = append(entries, files...)
	fp.entries = entries
	fp.cursor = 0
	fp.offset = 0
}

// navigateTo changes the current directory and reloads the listing.
func (fp *customFilePicker) navigateTo(dir string) {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	fp.currentDir = dir
	fp.loadDir()
}

// adjustOffset keeps the cursor inside the visible window.
func (fp *customFilePicker) adjustOffset() {
	if fp.cursor < fp.offset {
		fp.offset = fp.cursor
	} else if fp.height > 0 && fp.cursor >= fp.offset+fp.height {
		fp.offset = fp.cursor - fp.height + 1
	}
	if fp.offset < 0 {
		fp.offset = 0
	}
}

// update processes a single keypress.
// Returns (selectedPath, true) when the user confirms a selectable file;
// otherwise ("", false).
func (fp *customFilePicker) update(key string) (string, bool) {
	n := len(fp.entries)
	if n == 0 {
		return "", false
	}

	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}

	switch key {
	// ── Cursor movement ───────────────────────────────────────────────────────
	case "up", "k", "ctrl+p":
		fp.cursor = clamp(fp.cursor-1, 0, n-1)
		fp.adjustOffset()

	case "down", "j", "ctrl+n":
		fp.cursor = clamp(fp.cursor+1, 0, n-1)
		fp.adjustOffset()

	case "g":
		fp.cursor = 0
		fp.offset = 0

	case "G":
		fp.cursor = n - 1
		fp.adjustOffset()

	case "K", "pgup":
		fp.cursor = clamp(fp.cursor-fp.height, 0, n-1)
		fp.adjustOffset()

	case "J", "pgdown":
		fp.cursor = clamp(fp.cursor+fp.height, 0, n-1)
		fp.adjustOffset()

	// ── Open / select ─────────────────────────────────────────────────────────
	case "enter", "right", "l":
		e := fp.entries[fp.cursor]
		switch {
		case e.isDot && e.name == "..":
			if p := filepath.Dir(fp.currentDir); p != fp.currentDir {
				fp.navigateTo(p)
			}
		case e.isDot: // "." — stay
		case e.info.IsDir():
			fp.navigateTo(filepath.Join(fp.currentDir, e.name))
		case selectableExts[strings.ToLower(filepath.Ext(e.name))]:
			return filepath.Join(fp.currentDir, e.name), true
		}

	// ── Go up ─────────────────────────────────────────────────────────────────
	case "backspace", "left", "h", "esc":
		if p := filepath.Dir(fp.currentDir); p != fp.currentDir {
			fp.navigateTo(p)
		}
	}
	return "", false
}

// ── View ──────────────────────────────────────────────────────────────────────

// View renders the listing in ls -la style.
func (fp customFilePicker) View() string {
	if len(fp.entries) == 0 {
		return mutedStyle.Render("  (empty directory)")
	}

	// Pre-compute column widths for tight alignment.
	type colData struct {
		nlink int
		owner string
		group string
	}
	cols := make([]colData, len(fp.entries))
	nlinkW, ownerW, groupW, sizeW := 1, 1, 1, 1
	for i, e := range fp.entries {
		nl, ow, gr := fileOwnership(e.info)
		cols[i] = colData{nl, ow, gr}
		if w := len(strconv.Itoa(nl)); w > nlinkW {
			nlinkW = w
		}
		if len(ow) > ownerW {
			ownerW = len(ow)
		}
		if len(gr) > groupW {
			groupW = len(gr)
		}
		if w := len(strconv.FormatInt(e.info.Size(), 10)); w > sizeW {
			sizeW = w
		}
	}

	end := fp.offset + fp.height
	if end > len(fp.entries) {
		end = len(fp.entries)
	}

	lines := make([]string, 0, end-fp.offset)
	for i := fp.offset; i < end; i++ {
		c := cols[i]
		lines = append(lines, fp.formatEntry(
			fp.entries[i], c.nlink, c.owner, c.group,
			i == fp.cursor,
			nlinkW, ownerW, groupW, sizeW,
		))
	}
	return strings.Join(lines, "\n")
}

func (fp customFilePicker) formatEntry(
	e fileEntry,
	nlink int, owner, group string,
	selected bool,
	nlinkW, ownerW, groupW, sizeW int,
) string {
	mode := e.info.Mode().String()
	date := fmtModTime(e.info.ModTime())
	text := fmt.Sprintf("%s %*d %-*s %-*s %*d %s %s",
		mode,
		nlinkW, nlink,
		ownerW, owner,
		groupW, group,
		sizeW, e.info.Size(),
		date,
		e.name,
	)

	isSelectable := !e.isDot && !e.info.IsDir() &&
		selectableExts[strings.ToLower(filepath.Ext(e.name))]

	// Base colour
	var style lipgloss.Style
	switch {
	case e.isDot:
		style = dimStyle
	case e.info.IsDir():
		style = lipgloss.NewStyle().Foreground(clrBlue)
	case isSelectable:
		style = lipgloss.NewStyle().Foreground(clrWhite)
	default:
		style = dimStyle
	}

	if selected {
		return greenBoldStyle.Render("▶ ") + style.Copy().Bold(true).Render(text)
	}
	return "  " + style.Render(text)
}

// ── OS helpers ────────────────────────────────────────────────────────────────

// fileOwnership returns the hard-link count, owner name, and group name for a
// file.  Falls back to numeric IDs when names cannot be resolved.
func fileOwnership(info os.FileInfo) (nlink int, owner, group string) {
	nlink = 1
	owner, group = "?", "?"
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	nlink = int(stat.Nlink)
	uid := strconv.FormatUint(uint64(stat.Uid), 10)
	gid := strconv.FormatUint(uint64(stat.Gid), 10)
	if u, err := user.LookupId(uid); err == nil {
		owner = u.Username
	} else {
		owner = uid
	}
	if g, err := user.LookupGroupId(gid); err == nil {
		group = g.Name
	} else {
		group = gid
	}
	return
}

// fmtModTime formats a modification time like ls -la:
// within ~6 months → "Jan  2 15:04", older → "Jan  2  2006".
func fmtModTime(t time.Time) string {
	if time.Since(t) < 182*24*time.Hour {
		return t.Format("Jan _2 15:04")
	}
	return t.Format("Jan _2  2006")
}
