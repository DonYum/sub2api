//go:build unit

package service

import "strings"

// Fork policy (#17): an Anthropic stream that ends with end_turn and no
// assistant output is treated as empty and fails over instead of reaching the
// client. Upstream usage/model boundary tests use content-less streams, so the
// API-key passthrough routes need a real text block to exercise the path they
// were written for.
const anthropicTextOutputEventsForFailoverTest = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"

// withAnthropicTextOutputForFailoverTest inserts a text block right after the
// first SSE event (message_start).
func withAnthropicTextOutputForFailoverTest(sse string) string {
	i := strings.Index(sse, "\n\n")
	if i < 0 {
		return sse
	}
	return sse[:i+2] + anthropicTextOutputEventsForFailoverTest + sse[i+2:]
}
