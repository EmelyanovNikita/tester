package scenario

import (
	"testing"

	"tester_mm/internal/protoloader"
)

// loadTestProto загружает testdata/test.proto: сервис test.TestService с методом Echo
func loadTestProto(t *testing.T) *protoloader.ProtoData {
	t.Helper()

	data, err := protoloader.LoadProto("testdata", "test.proto")
	if err != nil {
		t.Fatalf("LoadProto: %v", err)
	}

	return data
}
