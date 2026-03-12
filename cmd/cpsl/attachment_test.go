package main

import (
	"encoding/json"
	"testing"
)

func TestExpandAttachments_NoStore(t *testing.T) {
	got := expandAttachments("hello world", nil)
	if got != "hello world" {
		t.Fatalf("expected passthrough, got %q", got)
	}
}

func TestExpandAttachments_NoPlaceholders(t *testing.T) {
	store := map[int]Attachment{1: {Data: "abc", MediaType: "image/png", IsImage: true}}
	got := expandAttachments("hello world", store)
	if got != "hello world" {
		t.Fatalf("expected passthrough, got %q", got)
	}
}

func TestExpandAttachments_SingleImage(t *testing.T) {
	store := map[int]Attachment{
		1: {Data: "AAAA", MediaType: "image/png", IsImage: true},
	}
	got := expandAttachments("Look at this: [Image #1] ok?", store)

	var blocks []map[string]string
	if err := json.Unmarshal([]byte(got), &blocks); err != nil {
		t.Fatalf("not valid JSON: %v\ngot: %s", err, got)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d: %v", len(blocks), blocks)
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "Look at this: " {
		t.Errorf("block 0: %v", blocks[0])
	}
	if blocks[1]["type"] != "image" || blocks[1]["media_type"] != "image/png" || blocks[1]["data"] != "AAAA" {
		t.Errorf("block 1: %v", blocks[1])
	}
	if blocks[2]["type"] != "text" || blocks[2]["text"] != " ok?" {
		t.Errorf("block 2: %v", blocks[2])
	}
}

func TestExpandAttachments_SingleFile(t *testing.T) {
	store := map[int]Attachment{
		1: {Data: "BBBB", MediaType: "application/pdf", IsImage: false},
	}
	got := expandAttachments("[File #1]", store)

	var blocks []map[string]string
	if err := json.Unmarshal([]byte(got), &blocks); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0]["type"] != "document" || blocks[0]["media_type"] != "application/pdf" {
		t.Errorf("block 0: %v", blocks[0])
	}
}

func TestExpandAttachments_Multiple(t *testing.T) {
	store := map[int]Attachment{
		1: {Data: "IMG1", MediaType: "image/png", IsImage: true},
		2: {Data: "PDF1", MediaType: "application/pdf", IsImage: false},
	}
	got := expandAttachments("[Image #1] and [File #2]", store)

	var blocks []map[string]string
	if err := json.Unmarshal([]byte(got), &blocks); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	if blocks[0]["type"] != "image" {
		t.Errorf("block 0 type: %s", blocks[0]["type"])
	}
	if blocks[1]["type"] != "text" || blocks[1]["text"] != " and " {
		t.Errorf("block 1: %v", blocks[1])
	}
	if blocks[2]["type"] != "document" {
		t.Errorf("block 2 type: %s", blocks[2]["type"])
	}
}

func TestExpandAttachments_MissingID(t *testing.T) {
	store := map[int]Attachment{
		1: {Data: "IMG1", MediaType: "image/png", IsImage: true},
	}
	// [Image #99] not in store — should be kept as text
	got := expandAttachments("hello [Image #99] world", store)

	var blocks []map[string]string
	if err := json.Unmarshal([]byte(got), &blocks); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	if blocks[0]["text"] != "hello " {
		t.Errorf("block 0: %v", blocks[0])
	}
	if blocks[1]["text"] != "[Image #99]" {
		t.Errorf("block 1: %v", blocks[1])
	}
	if blocks[2]["text"] != " world" {
		t.Errorf("block 2: %v", blocks[2])
	}
}

func TestExpandAttachments_TextOnly(t *testing.T) {
	store := map[int]Attachment{
		1: {Data: "IMG1", MediaType: "image/png", IsImage: true},
	}
	// Only text, no placeholders — but store is non-empty
	got := expandAttachments("just text", store)
	if got != "just text" {
		t.Fatalf("expected passthrough, got %q", got)
	}
}
