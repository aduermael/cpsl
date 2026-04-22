// subagent_output.go handles sub-agent result formatting and output
// persistence: graceful synthesis, result building, summarization (truncation
// and model-assisted), and the per-agent output file under .herm/agents/.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"langdag.com/langdag"
)

// gracefulSubAgentSynthesisOptions is the parameter bundle for (*SubAgentTool).gracefulSubAgentSynthesis.
type gracefulSubAgentSynthesisOptions struct {
	agent      *Agent
	lastNodeID string
}

// gracefulSubAgentSynthesis makes a tools-disabled LLM call so the sub-agent
// produces a text summary when it exceeded its turn budget while still requesting
// tools. Returns the synthesis text, or "" on failure. Uses a fresh context since
// the agent's context was canceled.
func (t *SubAgentTool) gracefulSubAgentSynthesis(ctx context.Context, opts gracefulSubAgentSynthesisOptions) string {
	if opts.lastNodeID == "" || t.client == nil {
		return ""
	}

	// Use a fresh context — the agent's context was canceled.
	synthCtx, synthCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer synthCancel()

	model := opts.agent.model
	// Note: WithSystemPrompt is ignored by PromptFrom (langdag uses the root
	// node's stored prompt), but included for documentation and forward compat.
	promptOpts := []langdag.PromptOption{
		langdag.WithSystemPrompt(opts.agent.systemPrompt),
		langdag.WithMaxTokens(defaultMaxOutputTokens),
		langdag.WithMaxOutputGroupTokens(defaultMaxOutputGroupTokens),
		// No WithTools — forces a text-only response.
	}
	if model != "" {
		promptOpts = append(promptOpts, langdag.WithModel(model))
	}

	// The model is told this is its final turn — budget numbers add nothing.
	synthMsg := synthesisPrompt("Turn limit reached")

	result, err := t.client.PromptFrom(synthCtx, opts.lastNodeID, synthMsg, promptOpts...)
	if err != nil {
		debugLog("gracefulSubAgentSynthesis failed: %v", err)
		return ""
	}

	// Drain the stream to collect text.
	var parts []string
	for chunk := range result.Stream {
		if chunk.Error != nil {
			debugLog("gracefulSubAgentSynthesis stream error: %v", chunk.Error)
			break
		}
		if chunk.Done {
			break
		}
		if chunk.Content != "" {
			parts = append(parts, chunk.Content)
		}
	}
	return strings.Join(parts, "")
}

// buildResultOptions is the parameter bundle for (*SubAgentTool).buildResult.
type buildResultOptions struct {
	agentID       string
	textParts     []string
	agentErrors   []string
	turns         int
	maxTurns      int
	synthesisUsed bool
}

// buildResult constructs the final tool result from collected sub-agent state.
// When synthesisUsed is true (the agent produced a structured synthesis via
// gracefulSubAgentSynthesis), the output is already summary-shaped and we skip
// the post-hoc model summarization call.
func (t *SubAgentTool) buildResult(ctx context.Context, opts buildResultOptions) string {
	result := strings.TrimSpace(strings.Join(opts.textParts, ""))
	if result == "" && len(opts.agentErrors) > 0 {
		// No text output but we have errors — use errors as the result body.
		result = "Sub-agent encountered errors:\n" + strings.Join(opts.agentErrors, "\n")
	} else if result == "" {
		result = "(sub-agent produced no output)"
	}
	outputPath := t.writeOutputFile(writeOutputFileOptions{agentID: opts.agentID, output: result})
	var summary string
	var usedModel bool
	if opts.synthesisUsed {
		// Synthesis already produced structured output — pass through or truncate.
		summary = summarizeOutput(result)
	} else {
		summary, usedModel = t.summarizeWithModel(ctx, result)
	}
	return formatSubAgentResult(formatSubAgentResultOptions{
		agentID:      opts.agentID,
		outputPath:   outputPath,
		summary:      summary,
		modelSummary: usedModel,
		turns:        opts.turns,
		maxTurns:     opts.maxTurns,
		errors:       opts.agentErrors,
	})
}

// formatSubAgentResultOptions is the parameter bundle for formatSubAgentResult.
type formatSubAgentResultOptions struct {
	agentID      string
	outputPath   string
	summary      string
	modelSummary bool
	turns        int
	maxTurns     int
	errors       []string
}

// formatSubAgentResult builds a compact tool result header with agent ID, turn
// count, summary quality indicator, and the summary body. Token counts are
// omitted (tracked via EventUsage, not actionable by the main agent).
func formatSubAgentResult(opts formatSubAgentResultOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[agent:%s turns:%d/%d", opts.agentID, opts.turns, opts.maxTurns)
	if opts.modelSummary {
		b.WriteString(" summary:model")
	} else if opts.outputPath != "" && len(opts.summary) > subAgentSummaryBytes {
		// Only mark truncated when there was actually more content (output file exists).
		b.WriteString(" summary:truncated")
	}
	b.WriteString("]")
	if opts.outputPath != "" {
		fmt.Fprintf(&b, " [output: %s]", opts.outputPath)
	}
	if len(opts.errors) > 0 {
		fmt.Fprintf(&b, " [errors: %s]", strings.Join(opts.errors, "; "))
	}
	fmt.Fprintf(&b, "\n\n%s", opts.summary)
	return b.String()
}

// subAgentSummaryBytes is the max bytes for the inline summary in the tool result.
// Outputs under this threshold pass through verbatim without model summarization.
// Set to 2KB so short results (~25-30 lines) avoid an unnecessary summarization call.
const subAgentSummaryBytes = 2000

// summarizeOutput returns output verbatim if within subAgentSummaryBytes,
// otherwise truncates at a line boundary and appends a note.
func summarizeOutput(s string) string {
	if len(s) <= subAgentSummaryBytes {
		return s
	}
	cut := s[:subAgentSummaryBytes]
	if i := strings.LastIndex(cut, "\n"); i > 0 {
		cut = cut[:i]
	}
	return cut + "\n[... full output in file above]"
}

// summarizeWithModelMaxChars is the max characters of sub-agent output to send
// to the exploration model for summarization. Set to 8KB so the summarizer sees
// enough context for accurate bullets even on longer outputs.
const summarizeWithModelMaxChars = 8000

// summarizeWithModelPrompt is the prompt sent to the exploration model for
// generating a structured summary of a sub-agent's output. The format gives
// the main agent machine-parseable structure while keeping content human-readable.
const summarizeWithModelPrompt = `Summarize this sub-agent output using exactly this format. No preamble, no extra commentary.

STATUS: success | partial | failure
FILES: <comma-separated key files touched or discovered, or "none">
FINDINGS:
- <bullet 1>
- <bullet 2>
- <bullet 3>
NEXT: <one-line recommendation for the caller, or "none">

--- SUB-AGENT OUTPUT ---
`

// summarizeWithModel calls the exploration model to generate a structured
// summary of a sub-agent's output. Falls back to summarizeOutput() if the
// model is not set or the call fails. Returns the summary and whether the
// model was used (true) or truncation fallback (false).
func (t *SubAgentTool) summarizeWithModel(ctx context.Context, output string) (string, bool) {
	// Short outputs don't need model summarization.
	if len(output) <= subAgentSummaryBytes {
		return output, false
	}

	// No exploration model configured — fall back to truncation.
	if t.explorationModel == "" || t.client == nil {
		return summarizeOutput(output), false
	}

	// Truncate the input to the model to keep costs low.
	modelInput := output
	if len(modelInput) > summarizeWithModelMaxChars {
		modelInput = modelInput[:summarizeWithModelMaxChars]
		if i := strings.LastIndex(modelInput, "\n"); i > 0 {
			modelInput = modelInput[:i]
		}
		modelInput += "\n[... truncated]"
	}

	summary, err := callLLMDirect(ctx, callLLMDirectOptions{client: t.client, model: t.explorationModel, prompt: summarizeWithModelPrompt + modelInput})
	if err != nil {
		debugLog("summarizeWithModel failed: %v", err)
		return summarizeOutput(output), false
	}

	summary = strings.TrimSpace(summary)
	if summary == "" {
		return summarizeOutput(output), false
	}

	return summary, true
}

// agentOutputDir returns the directory for sub-agent output files.
func agentOutputDir(workDir string) string {
	return filepath.Join(workDir, ".herm", "agents")
}

// writeOutputFileOptions is the parameter bundle for (*SubAgentTool).writeOutputFile.
type writeOutputFileOptions struct {
	agentID string
	output  string
}

// writeOutputFile writes the full sub-agent output to .herm/agents/<agentID>.md.
// Returns the file path on success, or empty string on failure (non-fatal).
func (t *SubAgentTool) writeOutputFile(opts writeOutputFileOptions) string {
	dir := agentOutputDir(t.workDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(dir, opts.agentID+".md")
	if err := os.WriteFile(path, []byte(opts.output), 0o644); err != nil {
		return ""
	}
	return path
}

// cleanupAgentOutputDir removes agent output files older than 24 hours.
func cleanupAgentOutputDir(workDir string) {
	dir := agentOutputDir(workDir)
	entries, err := os.ReadDir(dir)
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
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
