// main.go provides a JSON-driven file editor that performs exact string
// replacements and returns a unified diff of the changes.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Input struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

type Output struct {
	OK    bool   `json:"ok"`
	Diff  string `json:"diff,omitempty"`
	Error string `json:"error,omitempty"`
}

func main() {
	var in Input
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		writeError("invalid JSON input: " + err.Error())
		return
	}

	if in.FilePath == "" {
		writeError("file_path is required")
		return
	}
	if in.OldString == "" {
		writeError("old_string is required")
		return
	}
	if in.OldString == in.NewString {
		writeError("old_string and new_string are identical — nothing to change")
		return
	}

	content, err := os.ReadFile(in.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError("file not found: " + in.FilePath)
		} else {
			writeError("cannot read file: " + err.Error())
		}
		return
	}

	text := string(content)
	count := strings.Count(text, in.OldString)

	if count == 0 {
		writeError("old_string not found in " + in.FilePath)
		return
	}
	if count > 1 && !in.ReplaceAll {
		writeError(fmt.Sprintf("old_string found %d times in %s — use replace_all or provide a more specific string", count, in.FilePath))
		return
	}

	var newText string
	if in.ReplaceAll {
		newText = strings.ReplaceAll(text, in.OldString, in.NewString)
	} else {
		newText = strings.Replace(text, in.OldString, in.NewString, 1)
	}

	// Write atomically: write to temp file then rename.
	dir := filepath.Dir(in.FilePath)
	tmp, err := os.CreateTemp(dir, ".edit-file-*")
	if err != nil {
		writeError("cannot create temp file: " + err.Error())
		return
	}
	tmpName := tmp.Name()

	if _, err := tmp.WriteString(newText); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		writeError("cannot write temp file: " + err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		writeError("cannot close temp file: " + err.Error())
		return
	}

	// Preserve original file permissions.
	info, err := os.Stat(in.FilePath)
	if err == nil {
		os.Chmod(tmpName, info.Mode())
	}

	if err := os.Rename(tmpName, in.FilePath); err != nil {
		os.Remove(tmpName)
		writeError("cannot write file: " + err.Error())
		return
	}

	diff := unifiedDiff(unifiedDiffOptions{path: in.FilePath, a: text, b: newText})
	writeJSON(Output{OK: true, Diff: diff})
}

func writeError(msg string) {
	writeJSON(Output{OK: false, Error: msg})
}

func writeJSON(out Output) {
	json.NewEncoder(os.Stdout).Encode(out)
}

type unifiedDiffOptions struct {
	path string
	a, b string
}

// unifiedDiff produces a unified diff between two strings.
func unifiedDiff(opts unifiedDiffOptions) string {
	aLines := splitLines(opts.a)
	bLines := splitLines(opts.b)

	// Myers diff algorithm — compute shortest edit script.
	edits := myersDiff(myersDiffOptions{a: aLines, b: bLines})

	// Group edits into hunks with 3 lines of context.
	hunks := buildHunks(buildHunksOptions{edits: edits, context: 3})
	if len(hunks) == 0 {
		return ""
	}

	var sb strings.Builder
	// Strip leading / for cleaner diff headers.
	display := opts.path
	if strings.HasPrefix(display, "/") {
		display = display[1:]
	}
	sb.WriteString("--- a/" + display + "\n")
	sb.WriteString("+++ b/" + display + "\n")

	for _, h := range hunks {
		sb.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", h.aStart+1, h.aCount, h.bStart+1, h.bCount))
		for _, line := range h.lines {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// Preserve trailing newline semantics: if the string ends with \n,
	// Split produces an empty final element — remove it.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type editOp int

const (
	opEqual  editOp = iota
	opDelete        // line in a only
	opInsert        // line in b only
)

type edit struct {
	op   editOp
	line string
}

type myersDiffOptions struct {
	a, b []string
}

// myersDiff computes the shortest edit script between two line slices
// using the Myers O(ND) algorithm.
func myersDiff(opts myersDiffOptions) []edit {
	a, b := opts.a, opts.b
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	if n == 0 {
		edits := make([]edit, m)
		for i, l := range b {
			edits[i] = edit{opInsert, l}
		}
		return edits
	}
	if m == 0 {
		edits := make([]edit, n)
		for i, l := range a {
			edits[i] = edit{opDelete, l}
		}
		return edits
	}

	max := n + m
	off := max // offset so v[k] maps to v[off+k]
	size := 2*max + 1
	v := make([]int, size)
	v[off+1] = 0

	// trace[d] = snapshot of v taken at the START of iteration d
	// (i.e., v after all modifications through step d-1).
	trace := make([][]int, 0, max)

	for d := 0; d <= max; d++ {
		vc := make([]int, size)
		copy(vc, v)
		trace = append(trace, vc)

		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1] // move down (insert)
			} else {
				x = v[off+k-1] + 1 // move right (delete)
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(backtrackOptions{trace: trace, a: a, b: b, d: d, off: off})
			}
		}
	}
	return nil
}

type backtrackOptions struct {
	trace  [][]int
	a, b   []string
	d, off int
}

func backtrack(opts backtrackOptions) []edit {
	a, b := opts.a, opts.b
	edits := make([]edit, 0, len(a)+len(b))
	x, y := len(a), len(b)

	for dd := opts.d; dd > 0; dd-- {
		// trace[dd] = v at the start of step dd = v after step dd-1
		v := opts.trace[dd]
		k := x - y
		var prevK int
		if k == -dd || (k != dd && v[opts.off+k-1] < v[opts.off+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[opts.off+prevK]
		prevY := prevX - prevK

		// Diagonal moves (equal lines).
		for x > prevX && y > prevY {
			x--
			y--
			edits = append(edits, edit{opEqual, a[x]})
		}
		// The single edit move.
		if x == prevX {
			y--
			edits = append(edits, edit{opInsert, b[y]})
		} else {
			x--
			edits = append(edits, edit{opDelete, a[x]})
		}
	}
	// Remaining diagonal at d=0.
	for x > 0 && y > 0 {
		x--
		y--
		edits = append(edits, edit{opEqual, a[x]})
	}

	// Reverse to get forward order.
	for i, j := 0, len(edits)-1; i < j; i, j = i+1, j-1 {
		edits[i], edits[j] = edits[j], edits[i]
	}
	return edits
}

type hunk struct {
	aStart int
	aCount int
	bStart int
	bCount int
	lines  []string
}

type buildHunksOptions struct {
	edits   []edit
	context int
}

func buildHunks(opts buildHunksOptions) []hunk {
	edits, context := opts.edits, opts.context
	if len(edits) == 0 {
		return nil
	}

	// Find change regions (non-equal edits).
	type region struct {
		start, end int // indices into edits
	}
	var regions []region
	i := 0
	for i < len(edits) {
		if edits[i].op != opEqual {
			start := i
			for i < len(edits) && edits[i].op != opEqual {
				i++
			}
			regions = append(regions, region{start, i})
		} else {
			i++
		}
	}

	if len(regions) == 0 {
		return nil
	}

	// Build hunks: expand each region by context lines, merge overlapping.
	var hunks []hunk
	for _, r := range regions {
		cStart := r.start - context
		if cStart < 0 {
			cStart = 0
		}
		cEnd := r.end + context
		if cEnd > len(edits) {
			cEnd = len(edits)
		}

		// Check if we can merge with the previous hunk.
		if len(hunks) > 0 {
			prev := &hunks[len(hunks)-1]
			// Compute the edit index where the previous hunk ends.
			prevEnd := prevHunkEditEnd(prevHunkEditEndOptions{edits: edits, hunk: prev})
			if cStart <= prevEnd {
				// Merge: extend the previous hunk.
				extendHunk(extendHunkOptions{hunk: prev, edits: edits, from: prevEnd, to: cEnd})
				continue
			}
		}

		h := newHunk(newHunkOptions{edits: edits, start: cStart, end: cEnd})
		hunks = append(hunks, h)
	}
	return hunks
}

type newHunkOptions struct {
	edits      []edit
	start, end int
}

func newHunk(opts newHunkOptions) hunk {
	edits, start, end := opts.edits, opts.start, opts.end
	var h hunk
	// Compute a/b line positions at start.
	aLine, bLine := 0, 0
	for i := 0; i < start; i++ {
		switch edits[i].op {
		case opEqual:
			aLine++
			bLine++
		case opDelete:
			aLine++
		case opInsert:
			bLine++
		}
	}
	h.aStart = aLine
	h.bStart = bLine

	for i := start; i < end; i++ {
		switch edits[i].op {
		case opEqual:
			h.lines = append(h.lines, " "+edits[i].line)
			h.aCount++
			h.bCount++
		case opDelete:
			h.lines = append(h.lines, "-"+edits[i].line)
			h.aCount++
		case opInsert:
			h.lines = append(h.lines, "+"+edits[i].line)
			h.bCount++
		}
	}
	return h
}

type prevHunkEditEndOptions struct {
	edits []edit
	hunk  *hunk
}

func prevHunkEditEnd(opts prevHunkEditEndOptions) int {
	edits, h := opts.edits, opts.hunk
	// Walk edits to find where this hunk's content ends.
	aLine, bLine := 0, 0
	for i := 0; i < len(edits); i++ {
		targetA := h.aStart + h.aCount
		targetB := h.bStart + h.bCount
		if aLine >= targetA && bLine >= targetB {
			return i
		}
		switch edits[i].op {
		case opEqual:
			aLine++
			bLine++
		case opDelete:
			aLine++
		case opInsert:
			bLine++
		}
	}
	return len(edits)
}

type extendHunkOptions struct {
	hunk     *hunk
	edits    []edit
	from, to int
}

func extendHunk(opts extendHunkOptions) {
	h, edits := opts.hunk, opts.edits
	for i := opts.from; i < opts.to; i++ {
		switch edits[i].op {
		case opEqual:
			h.lines = append(h.lines, " "+edits[i].line)
			h.aCount++
			h.bCount++
		case opDelete:
			h.lines = append(h.lines, "-"+edits[i].line)
			h.aCount++
		case opInsert:
			h.lines = append(h.lines, "+"+edits[i].line)
			h.bCount++
		}
	}
}
