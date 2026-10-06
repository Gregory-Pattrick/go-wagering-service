package config

import "testing"

func TestTracingConfigRejectsInvalidEndpointAndRatio(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector:4318/v1/traces")
	t.Setenv("TRACING_SAMPLE_RATIO", "1")
	if _, e := LoadTracing(); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{"NaN", "+Inf", "-0.1", "1.1", ""} {
		t.Setenv("TRACING_SAMPLE_RATIO", bad)
		if _, e := LoadTracing(); e == nil {
			t.Fatalf("accepted ratio %q", bad)
		}
	}
	t.Setenv("TRACING_SAMPLE_RATIO", "0.5")
	for _, bad := range []string{"http://user:secret@collector:4318/v1/traces", "http://collector:4318/v1/traces?secret=x", "file:///v1/traces", "http://collector:4318/"} {
		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", bad)
		if _, e := LoadTracing(); e == nil {
			t.Fatalf("accepted endpoint %q", bad)
		}
	}
}
