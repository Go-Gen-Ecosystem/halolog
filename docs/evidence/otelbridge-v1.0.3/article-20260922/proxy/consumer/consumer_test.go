package consumer

import (
	"testing"

	"github.com/go-gen-ecosystem/halolog/otelbridge"
)

func TestPublishedBridgeConstructs(t *testing.T) {
	if adapter := otelbridge.NewAdapter("example.com/consumer"); adapter == nil {
		t.Fatal("published bridge returned a nil adapter")
	}
}
