# File & Image Attachments

Add support for attaching images and files to chat messages via drag-and-drop, paste, or clipboard.

## Langdag Dependency Note

No changes to langdag are needed for this feature. The existing `contentToRawMessage()` in `conversation.go:267` already passes JSON arrays through as content block arrays, and `types.ContentBlock` already supports `type:"image"` (with `media_type` + `data`) and `type:"document"`. All provider protocols (Anthropic, OpenAI, Gemini, Grok) already map these to their native image formats. Everything is app-layer work in CPSL.

If langdag changes become necessary during implementation, spec them out here for the langdag team rather than working around them in CPSL.

## Codebase Context

- **Input handling**: `main.go` — `handlePaste()` receives bracketed paste content as a string, already supports collapsing large pastes with `[pasted #N | N chars]` placeholders
- **Message sending**: `main.go:2148` — `startAgent(content)` passes a plain string to `Agent.Run()`, which passes it to `langdag.Client.Prompt()`/`PromptFrom()`
- **langdag content blocks**: `contentToRawMessage()` in langdag's `conversation.go:267` already detects JSON arrays and passes them through as content block arrays. So if the message string is a JSON array like `[{"type":"text","text":"..."}, {"type":"image","media_type":"image/png","data":"..."}]`, it's used as-is
- **Supported block types**: `types.ContentBlock` supports `type:"image"` with `media_type` + `data` (base64), and `type:"document"` with `media_type` + `data`
- **App state**: `pasteStore map[int]string` holds collapsed paste content; similar pattern for attachments
- **Project dir**: `.cpsl/` exists per-project for config, skills, history

## Contracts & Interfaces

### Attachment store (App fields)
- `attachments map[int]Attachment` — maps attachment ID to file metadata + base64 data
- `attachmentCount int` — monotonic counter for `[Image #N]` / `[File #N]` placeholders

### Attachment struct
- `Path string` — original file path (may be temp path for clipboard images)
- `MediaType string` — MIME type (e.g. `image/png`, `application/pdf`)
- `Data string` — base64-encoded file content
- `IsImage bool` — whether this is an image attachment

### Placeholder format
- Images: `[Image #1]`, `[Image #2]`, ...
- Files: `[File #1]`, `[File #2]`, ...
- These appear inline in the input buffer, similar to existing `[pasted #N | N chars]`

### Message expansion
When sending, `expandAttachments()` converts the message text + attachment placeholders into a JSON content block array:
```json
[
  {"type": "text", "text": "Look at this:"},
  {"type": "image", "media_type": "image/png", "data": "base64..."},
  {"type": "text", "text": "What do you see?"}
]
```

## Phase 1: File path detection in paste/drop
- [x] 1a: Add `Attachment` struct and attachment store fields to `App` (`attachments`, `attachmentCount`)
- [x] 1b: Add `isFilePath()` helper that checks if a pasted string looks like an absolute file path (starts with `/` or `~`, exists on disk)
- [x] 1c: Add `isImageExt()` helper for image extensions (png, jpg, jpeg, gif, webp, bmp, tiff, svg)
- [ ] 1d: Add `mimeForExt()` helper that returns MIME type from file extension (image/png, image/jpeg, application/pdf, etc.)
- [ ] 1e: Modify `handlePaste()` — before the existing collapse logic, check if the entire content (trimmed, unquoted if shell-escaped) is a valid file path. If so, read + base64-encode the file, store in `attachments`, and insert `[Image #N]` or `[File #N]` placeholder instead of the raw path. Handle shell-escaped paths (e.g. backslash-spaces from terminal drag-drop)

## Phase 2: Message expansion to content blocks
- [ ] 2a: Add `expandAttachments()` function — takes message string + attachment store, splits on `[Image #N]` / `[File #N]` placeholders, returns either plain text (no attachments) or JSON content block array string. Text segments become `{"type":"text"}` blocks, attachments become `{"type":"image"}` or `{"type":"document"}` blocks
- [ ] 2b: Wire `expandAttachments()` into `handleEnter()` — after `expandPastes()`, call `expandAttachments()`. Pass the result to `startAgent()`. The display message (`chatMessage`) should still show the human-readable version with placeholders
- [ ] 2c: Clear `attachments` and `attachmentCount` on `resetInput()` or after message send (similar to `pasteStore`)

## Phase 3: Clipboard image paste (macOS)
- [ ] 3a: Add `clipboardHasImage()` function — uses `osascript` to check if the clipboard contains image data (class `«class PNGf»` or similar)
- [ ] 3b: Add `clipboardSaveImage()` function — uses `osascript`/`pbpaste` to write clipboard image data to a temp file under `.cpsl/tmp/`, returns the path. Create `.cpsl/tmp/` if it doesn't exist
- [ ] 3c: Detect clipboard image paste — when `handlePaste()` receives empty or non-path content AND `clipboardHasImage()` is true, call `clipboardSaveImage()`, then process the temp file as an attachment. This handles Cmd+V with a screenshot in the clipboard

## Phase 4: Multi-file and edge cases
- [ ] 4a: Handle multiple file paths in a single paste (newline-separated paths from dragging multiple files)
- [ ] 4b: Add file size limit check — reject files over a reasonable limit (e.g. 20MB) with an error message in the input, since they'll be base64-encoded in the API payload
- [ ] 4c: Add `.cpsl/tmp/` cleanup — delete temp clipboard images older than 24h on app startup

## Success Criteria
- Dragging a PNG from Finder into the input shows `[Image #1]` and the message reaches the LLM with the image as a base64 content block
- Dragging a PDF shows `[File #1]` and reaches the LLM as a document content block
- Pasting a screenshot (Cmd+V) when clipboard has image data creates a temp file and attaches it as `[Image #N]`
- Multiple files can be attached in one message (drag multiple, or multiple paste operations)
- Non-existent paths fall through to normal paste behavior
- Files over the size limit show an error instead of silently failing
