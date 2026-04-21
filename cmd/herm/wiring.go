// wiring.go holds initialization helpers split out of main.go: attachment
// ingestion, clipboard capture, tmp-dir cleanup, and async startup fan-out.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"langdag.com/langdag"
)

// tryAttachFile checks if s is a valid file path, reads and base64-encodes it,
// stores it in the attachment map, and returns the placeholder string.
func (a *App) tryAttachFile(s string) (string, bool) {
	resolved, ok := isFilePath(s)
	if !ok {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", false
	}
	if info.Size() > maxAttachmentBytes {
		return fmt.Sprintf("[file too large: %s (%d MB limit)]",
			filepath.Base(resolved), maxAttachmentBytes>>20), true
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", false
	}
	if a.attachments == nil {
		a.attachments = make(map[int]Attachment)
	}
	a.attachmentCount++
	isImg := isImageExt(resolved)
	a.attachments[a.attachmentCount] = Attachment{
		Path:      resolved,
		MediaType: mimeForExt(resolved),
		Data:      base64.StdEncoding.EncodeToString(data),
		IsImage:   isImg,
	}

	// Copy file to host attachment dir for container mount.
	if a.worktreePath != "" {
		dir := a.attachmentDir()
		if err := os.MkdirAll(dir, 0o755); err == nil {
			dst := filepath.Join(dir, filepath.Base(resolved))
			if _, err := os.Stat(dst); err == nil {
				// Collision — prepend attachment ID.
				dst = filepath.Join(dir, fmt.Sprintf("%d-%s", a.attachmentCount, filepath.Base(resolved)))
			}
			_ = os.WriteFile(dst, data, 0o644)
		}
	}

	if isImg {
		return fmt.Sprintf("[Image #%d]", a.attachmentCount), true
	}
	return fmt.Sprintf("[File #%d]", a.attachmentCount), true
}

// tryAttachPaths checks if val is one or more file paths (e.g. from
// drag-and-drop in terminals that don't use bracketed paste) and attaches
// them. Returns the modified string with attachment placeholders, or the
// original string unchanged if no paths were detected.
func (a *App) tryAttachPaths(val string) string {
	// Single file path.
	if placeholder, ok := a.tryAttachFile(val); ok {
		return placeholder
	}
	// Multiple newline-separated file paths.
	lines := strings.Split(val, "\n")
	if len(lines) <= 1 {
		return val
	}
	var placeholders []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p, ok := a.tryAttachFile(line)
		if !ok {
			return val // not all lines are file paths — return unchanged
		}
		placeholders = append(placeholders, p)
	}
	if len(placeholders) > 0 {
		return strings.Join(placeholders, " ")
	}
	return val
}

// attachmentDir returns the host path for this session's attachment files.
func (a *App) attachmentDir() string {
	return filepath.Join(a.worktreePath, ".herm", "attachments", a.sessionID)
}

// clipboardHasImage checks if the macOS clipboard contains image data.
func clipboardHasImage() bool {
	out, err := exec.Command("osascript", "-e",
		"clipboard info").Output()
	if err != nil {
		return false
	}
	// clipboard info returns lines like "«class PNGf», 12345"
	s := string(out)
	return strings.Contains(s, "PNGf") || strings.Contains(s, "TIFF") ||
		strings.Contains(s, "GIFf") || strings.Contains(s, "JPEG")
}

// clipboardSaveImage writes macOS clipboard image data to a temp PNG file
// under .herm/tmp/ and returns the file path.
func (a *App) clipboardSaveImage() (string, error) {
	tmpDir := filepath.Join(a.worktreePath, ".herm", "tmp")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("clipboard-%d.png", time.Now().UnixMilli())
	path := filepath.Join(tmpDir, name)

	script := fmt.Sprintf(`
		set f to POSIX file %q
		try
			set img to the clipboard as «class PNGf»
			set fh to open for access f with write permission
			write img to fh
			close access fh
		on error
			try
				close access f
			end try
			error "no image on clipboard"
		end try
	`, path)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// cleanupTmpDir removes files in .herm/tmp/ older than 24 hours.
func cleanupTmpDir(worktreePath string) {
	tmpDir := filepath.Join(worktreePath, ".herm", "tmp")
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(tmpDir, e.Name()))
		}
	}
}

// handleUpdateCommand handles the /update slash command.
func (a *App) handleUpdateCommand() {
	if Version == "dev" {
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "Update check is not available for development builds."})
		a.render()
		return
	}
	if a.updateAvailable == "" {
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: fmt.Sprintf("Already up to date (v%s).", strings.TrimPrefix(Version, "v"))})
		a.render()
		return
	}
	ver := a.updateAvailable
	a.messages = append(a.messages, chatMessage{kind: msgInfo, content: fmt.Sprintf("Downloading v%s...", ver)})
	a.render()
	go func() { a.resultCh <- performUpdate(ver) }()
}

// startInit fans out the async startup jobs (workspace resolve, client init,
// catalog load, update check, SWE scores) whose results are consumed by
// drainResults.
func (a *App) startInit() {
	cfg := a.config
	go func() { a.resultCh <- fetchSWEScoresCmd() }()
	go func() { a.resultCh <- resolveWorkspaceCmd(cfg) }()
	go func() {
		client, err := newLangdagClient(cfg)
		a.resultCh <- langdagReadyMsg{client: client, provider: cfg.defaultLangdagProvider(), err: err}
	}()
	go func() {
		cachePath := catalogCachePath()
		catalog, err := langdag.LoadModelCatalog(cachePath)
		if err != nil {
			log.Printf("warning: loading model catalog: %v", err)
		}
		a.resultCh <- catalogMsg{catalog: catalog}

		// Best-effort background refresh of the cache
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if updated, err := langdag.FetchModelCatalog(ctx, cachePath); err == nil {
			a.resultCh <- catalogMsg{catalog: updated}
		}
	}()
	go func() { a.resultCh <- checkForUpdate(Version) }()
}
