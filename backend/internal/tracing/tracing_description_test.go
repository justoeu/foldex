package tracing

import (
	"context"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Description is what operators (and the SDK's own diagnostics) see for the
// sampler wired into Setup as ParentBased(rootServerSampler{}); pin both the
// root sampler's own string and the composed form the provider reports.
func TestRootServerSamplerDescription(t *testing.T) {
	t.Parallel()
	if got := (rootServerSampler{}).Description(); got != "RootServerOnly" {
		t.Fatalf("Description() = %q, want %q", got, "RootServerOnly")
	}
	composed := sdktrace.ParentBased(rootServerSampler{}).Description()
	if !strings.Contains(composed, "ParentBased{root:RootServerOnly,") {
		t.Fatalf("ParentBased(rootServerSampler{}).Description() = %q, want it to embed the root sampler's description", composed)
	}
}

// The sampling decision itself, unit-level: only SERVER roots are sampled.
// The exporter-level behaviour is covered by
// TestSamplerDropsRootClientSpansKeepsServerTraces.
func TestRootServerSamplerShouldSampleByKind(t *testing.T) {
	t.Parallel()
	sampler := rootServerSampler{}
	cases := []struct {
		kind trace.SpanKind
		want sdktrace.SamplingDecision
	}{
		{kind: trace.SpanKindServer, want: sdktrace.RecordAndSample},
		{kind: trace.SpanKindClient, want: sdktrace.Drop},
		{kind: trace.SpanKindInternal, want: sdktrace.Drop},
		{kind: trace.SpanKindProducer, want: sdktrace.Drop},
		{kind: trace.SpanKindConsumer, want: sdktrace.Drop},
	}
	for _, tc := range cases {
		got := sampler.ShouldSample(sdktrace.SamplingParameters{
			ParentContext: context.Background(),
			Kind:          tc.kind,
			Name:          "probe",
		})
		if got.Decision != tc.want {
			t.Errorf("kind %v: decision = %v, want %v", tc.kind, got.Decision, tc.want)
		}
	}
}
