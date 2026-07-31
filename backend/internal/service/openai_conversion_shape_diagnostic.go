package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const maxOpenAIShapeRecords = 256

type openAIJSONShapeSummary struct {
	Hash      string
	NodeCount int
	MaxDepth  int
	Records   []string
	Truncated bool
}

// Protocol field names are safe to expose as paths. Unknown keys may come from
// user-defined JSON schemas, so they are collapsed to a fixed placeholder.
var openAIShapeSafeKeys = map[string]struct{}{
	"additionalProperties": {}, "arguments": {}, "content": {}, "description": {},
	"call_id": {}, "children": {}, "detail": {}, "effort": {}, "encrypted_content": {},
	"format": {}, "frequency_penalty": {},
	"function": {}, "function_call": {}, "functions": {}, "id": {},
	"image_url": {}, "include": {}, "include_usage": {}, "input": {},
	"instructions": {}, "json_schema": {}, "logit_bias": {}, "logprobs": {},
	"max_completion_tokens": {}, "max_output_tokens": {}, "max_tokens": {},
	"messages": {}, "metadata": {}, "model": {}, "n": {}, "name": {},
	"output": {}, "parallel_tool_calls": {}, "parameters": {}, "presence_penalty": {},
	"previous_response_id": {},
	"prompt_cache_key":     {}, "properties": {}, "reasoning": {},
	"reasoning_content": {}, "reasoning_effort": {}, "required": {}, "response_format": {}, "role": {},
	"schema": {}, "seed": {}, "service_tier": {}, "stop": {}, "store": {},
	"stream": {}, "stream_options": {}, "strict": {}, "summary": {}, "temperature": {}, "text": {},
	"tool_call_id": {}, "tool_calls": {}, "tool_choice": {}, "tools": {},
	"top_logprobs": {}, "top_p": {}, "truncation": {}, "type": {}, "url": {},
	"user": {}, "verbosity": {},
}

func summarizeOpenAIJSONShape(body []byte) (openAIJSONShapeSummary, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return openAIJSONShapeSummary{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return openAIJSONShapeSummary{}, fmt.Errorf("multiple JSON values")
		}
		return openAIJSONShapeSummary{}, err
	}

	collector := openAIShapeCollector{
		records: make(map[string]int),
	}
	collector.walk("$", value, 0, openAIShapeKeysNormal)
	shapeHash := sha256.Sum256([]byte(openAIShapeHashToken(value, openAIShapeKeysNormal)))

	records := make([]string, 0, len(collector.records))
	for record, count := range collector.records {
		if count > 1 {
			record = fmt.Sprintf("%s count=%d", record, count)
		}
		records = append(records, record)
	}
	sort.Strings(records)
	truncated := len(records) > maxOpenAIShapeRecords
	if truncated {
		records = records[:maxOpenAIShapeRecords]
	}

	return openAIJSONShapeSummary{
		Hash:      hex.EncodeToString(shapeHash[:]),
		NodeCount: collector.nodeCount,
		MaxDepth:  collector.maxDepth,
		Records:   records,
		Truncated: truncated,
	}, nil
}

type openAIShapeCollector struct {
	records   map[string]int
	nodeCount int
	maxDepth  int
}

func (c *openAIShapeCollector) walk(path string, value any, depth int, keyMode openAIShapeKeyMode) {
	c.nodeCount++
	if depth > c.maxDepth {
		c.maxDepth = depth
	}

	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		c.addRecord(fmt.Sprintf("%s object fields=%d", path, len(keys)))
		for _, key := range keys {
			safeKey, childMode := openAIShapeField(keyMode, key)
			c.walk(path+"."+safeKey, typed[key], depth+1, childMode)
		}
	case []any:
		c.addRecord(fmt.Sprintf("%s array len=%d", path, len(typed)))
		for _, item := range typed {
			c.walk(path+"[]", item, depth+1, keyMode)
		}
	case string:
		c.addRecord(fmt.Sprintf("%s string bytes=%d", path, len([]byte(typed))))
	case json.Number:
		c.addRecord(fmt.Sprintf("%s number bytes=%d", path, len(typed.String())))
	case bool:
		c.addRecord(path + " boolean")
	case nil:
		c.addRecord(path + " null")
	default:
		c.addRecord(path + " unknown")
	}
}

func (c *openAIShapeCollector) addRecord(record string) {
	c.records[record]++
}

type openAIShapeKeyMode uint8

const (
	openAIShapeKeysNormal openAIShapeKeyMode = iota
	openAIShapeKeysSchemaProperties
	openAIShapeKeysUnknown
)

func openAIShapeField(mode openAIShapeKeyMode, key string) (string, openAIShapeKeyMode) {
	if mode == openAIShapeKeysSchemaProperties {
		return "<key>", openAIShapeKeysNormal
	}
	if mode == openAIShapeKeysUnknown {
		return "<key>", openAIShapeKeysUnknown
	}
	if _, ok := openAIShapeSafeKeys[key]; !ok {
		return "<key>", openAIShapeKeysUnknown
	}
	switch key {
	case "properties":
		return key, openAIShapeKeysSchemaProperties
	case "metadata", "logit_bias":
		return key, openAIShapeKeysUnknown
	default:
		return key, openAIShapeKeysNormal
	}
}

func openAIShapeHashToken(value any, keyMode openAIShapeKeyMode) string {
	switch typed := value.(type) {
	case map[string]any:
		fields := make([]string, 0, len(typed))
		for key, child := range typed {
			safeKey, childMode := openAIShapeField(keyMode, key)
			fields = append(fields, safeKey+":"+openAIShapeHashToken(child, childMode))
		}
		sort.Strings(fields)
		return fmt.Sprintf("object:%d{%s}", len(fields), strings.Join(fields, ","))
	case []any:
		items := make([]string, 0, len(typed))
		for _, child := range typed {
			items = append(items, openAIShapeHashToken(child, keyMode))
		}
		return fmt.Sprintf("array:%d[%s]", len(items), strings.Join(items, ","))
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

func logOpenAIConversionShapeDiagnostic(
	accountID int64,
	inboundBody []byte,
	outboundBody []byte,
) {
	inbound, inboundErr := summarizeOpenAIJSONShape(inboundBody)
	outbound, outboundErr := summarizeOpenAIJSONShape(outboundBody)
	if inboundErr != nil || outboundErr != nil {
		logger.L().Warn("openai.chat_to_responses_shape_diagnostic_failed",
			zap.Int64("account_id", accountID),
			zap.Bool("inbound_summary_failed", inboundErr != nil),
			zap.Bool("outbound_summary_failed", outboundErr != nil),
		)
		return
	}

	logger.L().Info("openai.chat_to_responses_shape_diagnostic",
		zap.Int64("account_id", accountID),
		zap.String("inbound_shape_hash", inbound.Hash),
		zap.Int("inbound_nodes", inbound.NodeCount),
		zap.Int("inbound_max_depth", inbound.MaxDepth),
		zap.Bool("inbound_records_truncated", inbound.Truncated),
		zap.Strings("inbound_shape", inbound.Records),
		zap.String("outbound_shape_hash", outbound.Hash),
		zap.Int("outbound_nodes", outbound.NodeCount),
		zap.Int("outbound_max_depth", outbound.MaxDepth),
		zap.Bool("outbound_records_truncated", outbound.Truncated),
		zap.Strings("outbound_shape", outbound.Records),
	)
}

// Temporary incident diagnostic authorized for task #121. It intentionally
// records full request/converted/upstream-error bodies only on the narrowly
// scoped Chat Completions -> Responses HTTP 400 path. Remove after diagnosis.
func logOpenAIConversionRawDiagnostic(
	accountID int64,
	inboundBody []byte,
	outboundBody []byte,
	upstreamErrorBody []byte,
	upstreamHeaders http.Header,
) {
	logOpenAIConversionShapeDiagnostic(accountID, inboundBody, outboundBody)
	logger.L().Warn("openai.chat_to_responses_raw_diagnostic",
		zap.Int64("account_id", accountID),
		zap.ByteString("inbound_body", inboundBody),
		zap.ByteString("outbound_body", outboundBody),
		zap.ByteString("upstream_error_body", upstreamErrorBody),
		zap.String("x_request_id", upstreamHeaders.Get("x-request-id")),
		zap.String("request_id", upstreamHeaders.Get("request-id")),
		zap.String("openai_request_id", upstreamHeaders.Get("openai-request-id")),
		zap.String("cf_ray", upstreamHeaders.Get("cf-ray")),
		zap.String("traceparent", upstreamHeaders.Get("traceparent")),
	)
}
