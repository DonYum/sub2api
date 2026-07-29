package repository

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestOpsErrorLogInsertDoesNotPersistRequestReplayFields(t *testing.T) {
	disallowedColumns := []string{
		"request_body",
		"request_headers",
		"request_body_truncated",
		"request_body_bytes",
		"is_retryable",
		"retry_count",
		"resolved_retry_id",
	}

	insertSQL := strings.ToLower(insertOpsErrorLogSQL)
	for _, column := range disallowedColumns {
		if strings.Contains(insertSQL, column) {
			t.Fatalf("ops error log insert still references dropped replay column %q", column)
		}
	}

	inputType := reflect.TypeOf(service.OpsInsertErrorLogInput{})
	disallowedFields := []string{
		"RequestBodyJSON",
		"RequestBodyTruncated",
		"RequestBodyBytes",
		"RequestHeadersJSON",
		"IsRetryable",
		"RetryCount",
		"ResolvedRetryID",
	}
	for _, field := range disallowedFields {
		if _, ok := inputType.FieldByName(field); ok {
			t.Fatalf("OpsInsertErrorLogInput still carries replay field %q", field)
		}
	}
}

func TestOpsErrorLogInsertPersistsProviderFieldsAndKeepsMissingValuesNull(t *testing.T) {
	input := &service.OpsInsertErrorLogInput{
		ProviderErrorCode: "server_is_overloaded",
		ProviderErrorType: "api_error",
	}
	args := opsInsertErrorLogArgs(input)
	if len(args) != 43 {
		t.Fatalf("ops insert args = %d, want 43", len(args))
	}
	code, ok := args[30].(sql.NullString)
	if !ok || !code.Valid || code.String != "server_is_overloaded" {
		t.Fatalf("provider_error_code arg = %#v", args[30])
	}
	errorType, ok := args[31].(sql.NullString)
	if !ok || !errorType.Valid || errorType.String != "api_error" {
		t.Fatalf("provider_error_type arg = %#v", args[31])
	}

	emptyArgs := opsInsertErrorLogArgs(&service.OpsInsertErrorLogInput{})
	for _, index := range []int{30, 31} {
		value, ok := emptyArgs[index].(sql.NullString)
		if !ok || value.Valid {
			t.Fatalf("empty provider arg %d = %#v, want SQL NULL", index, emptyArgs[index])
		}
	}
}
