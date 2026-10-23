package provider

import "github.com/qeinstein/paradox/harness"

// MetaModels reflects the model identifiers documented by Meta Model API at
// the time of implementation. The catalog is informational; the API remains
// the source of truth and ListModels queries it when credentials are present.
func MetaModels() []harness.ModelInfo {
	caps := harness.Capabilities{
		ToolCalls: true, StructuredOutput: true, Reasoning: true,
		Streaming: true, MaxContextTokens: 1048576,
	}
	models := []struct {
		id   string
		name string
	}{
		{"muse-spark-1.3", "Muse Spark 1.3"},
		{"muse-spark-1.2", "Muse Spark 1.2"},
		{"muse-spark-1.1", "Muse Spark 1.1"},
		{"muse-spark-1.3-contributor", "Muse Spark 1.3 Contributor"},
		{"muse-spark-1.2-contributor", "Muse Spark 1.2 Contributor"},
		{"muse-wonder-1.0", "Muse Wonder 1.0"},
	}
	out := make([]harness.ModelInfo, 0, len(models))
	for _, model := range models {
		out = append(out, harness.ModelInfo{
			ID: model.id, Provider: "meta", DisplayName: model.name, Capabilities: caps,
		})
	}
	return out
}

func DefaultModel(providerName string) string {
	switch providerName {
	case "meta":
		return "muse-spark-1.3"
	default:
		return ""
	}
}
