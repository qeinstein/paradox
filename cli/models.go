package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/qeinstein/paradox/harness"
	"github.com/qeinstein/paradox/provider"
)

type modelEntry struct {
	Model  harness.ModelInfo `json:"model"`
	Source string            `json:"source"`
}

type modelList struct {
	Models   []modelEntry `json:"models"`
	Warnings []string     `json:"warnings,omitempty"`
}

func modelsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerName := fs.String("provider", "all", "provider: all, mock, meta, openrouter, or openai-compatible")
	model := fs.String("model", "", "model id for openai-compatible listing")
	apiKey := fs.String("api-key", "", "provider API key (prefer environment variables)")
	baseURL := fs.String("base-url", "", "base URL for openai-compatible providers")
	remote := fs.Bool("remote", false, "query the provider's live /models endpoint where supported")
	limit := fs.Int("limit", 0, "maximum models to print (0 means all)")
	timeout := fs.Duration("timeout", 20*time.Second, "remote listing timeout")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "models: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	list, err := listModels(ctx, strings.ToLower(strings.TrimSpace(*providerName)), *model, *apiKey, *baseURL, *remote, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "models: %v\n", err)
		return 1
	}
	if *limit > 0 && len(list.Models) > *limit {
		list.Models = list.Models[:*limit]
	}
	for _, warning := range list.Warnings {
		fmt.Fprintf(stderr, "models: warning: %s\n", warning)
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(list); err != nil {
			fmt.Fprintf(stderr, "models: %v\n", err)
			return 1
		}
		return 0
	}
	if len(list.Models) == 0 {
		fmt.Fprintln(stderr, "models: no models available")
		return 1
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "PROVIDER\tMODEL\tNAME\tCONTEXT\tTOOLS\tREASONING\tSOURCE")
	for _, entry := range list.Models {
		info := entry.Model
		contextTokens := "unknown"
		if info.Capabilities.MaxContextTokens > 0 {
			contextTokens = fmt.Sprintf("%d", info.Capabilities.MaxContextTokens)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%t\t%t\t%s\n",
			info.Provider, info.ID, info.DisplayName, contextTokens,
			info.Capabilities.ToolCalls, info.Capabilities.Reasoning, entry.Source)
	}
	_ = writer.Flush()
	return 0
}

func listModels(ctx context.Context, name, model, apiKey, baseURL string, remote bool, timeout time.Duration) (modelList, error) {
	list := modelList{}
	key := strings.TrimSpace(apiKey)
	base := strings.TrimSpace(baseURL)
	switch name {
	case "all", "":
		list.Models = append(list.Models, modelEntry{Model: mockModel(), Source: "built_in"})
		list.Models = append(list.Models, staticMetaEntries()...)
		if remote {
			if metaKey := firstNonEmpty(key, envFirst("META_API_KEY", "MODEL_API_KEY")); metaKey != "" {
				models, err := fetchModels(ctx, provider.NewMeta(metaKey, firstNonEmpty(model, provider.DefaultModel("meta"))), timeout)
				if err != nil {
					list.Warnings = append(list.Warnings, "meta: "+err.Error())
				} else {
					list.Models = append(list.Models, entries(models, "meta_api")...)
				}
			}
			if openRouterKey := envFirst("OPENROUTER_API_KEY"); openRouterKey != "" {
				models, err := fetchModels(ctx, provider.NewOpenRouter(openRouterKey, firstNonEmpty(model, "openrouter/auto")), timeout)
				if err != nil {
					list.Warnings = append(list.Warnings, "openrouter: "+err.Error())
				} else {
					list.Models = append(list.Models, entries(models, "openrouter_api")...)
				}
			}
			if base != "" {
				client := provider.NewOpenAICompatible(provider.OpenAIConfig{
					Name: "openai-compatible", BaseURL: base, APIKey: key,
					Model: firstNonEmpty(model, "configured-model"),
				})
				models, err := fetchModels(ctx, client, timeout)
				if err != nil {
					list.Warnings = append(list.Warnings, "openai-compatible: "+err.Error())
				} else {
					list.Models = append(list.Models, entries(models, "openai_compatible_api")...)
				}
			}
		}
	case "mock":
		list.Models = []modelEntry{{Model: mockModel(), Source: "built_in"}}
	case "meta":
		if !remote {
			list.Models = staticMetaEntries()
			break
		}
		metaKey := firstNonEmpty(key, envFirst("META_API_KEY", "MODEL_API_KEY"))
		if metaKey == "" {
			return list, errors.New("Meta live listing requires META_API_KEY or MODEL_API_KEY")
		}
		models, err := fetchModels(ctx, provider.NewMeta(metaKey, firstNonEmpty(model, provider.DefaultModel("meta"))), timeout)
		if err != nil {
			return list, err
		}
		list.Models = entries(models, "meta_api")
	case "openrouter":
		openRouterKey := firstNonEmpty(key, envFirst("OPENROUTER_API_KEY"))
		if openRouterKey == "" {
			return list, errors.New("OpenRouter listing requires OPENROUTER_API_KEY")
		}
		models, err := fetchModels(ctx, provider.NewOpenRouter(openRouterKey, firstNonEmpty(model, "openrouter/auto")), timeout)
		if err != nil {
			return list, err
		}
		list.Models = entries(models, "openrouter_api")
	case "openai-compatible":
		if base == "" {
			return list, errors.New("openai-compatible listing requires --base-url")
		}
		client := provider.NewOpenAICompatible(provider.OpenAIConfig{
			Name: "openai-compatible", BaseURL: base, APIKey: key,
			Model: firstNonEmpty(model, "configured-model"),
		})
		if !remote {
			info := harness.ModelInfo{
				ID: firstNonEmpty(model, "configured-model"), Provider: "openai-compatible",
				DisplayName:  firstNonEmpty(model, "configured-model"),
				Capabilities: harness.Capabilities{ToolCalls: true, StructuredOutput: true, Reasoning: true},
			}
			list.Models = []modelEntry{{Model: info, Source: "configured"}}
			break
		}
		models, err := fetchModels(ctx, client, timeout)
		if err != nil {
			return list, err
		}
		list.Models = entries(models, "openai_compatible_api")
	default:
		return list, fmt.Errorf("unknown provider %q (want all, mock, meta, openrouter, or openai-compatible)", name)
	}
	sort.SliceStable(list.Models, func(i, j int) bool {
		if list.Models[i].Model.Provider == list.Models[j].Model.Provider {
			return list.Models[i].Model.ID < list.Models[j].Model.ID
		}
		return list.Models[i].Model.Provider < list.Models[j].Model.Provider
	})
	return list, nil
}

func fetchModels(ctx context.Context, lister provider.ModelLister, timeout time.Duration) ([]harness.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return lister.ListModels(ctx)
}

func entries(models []harness.ModelInfo, source string) []modelEntry {
	out := make([]modelEntry, 0, len(models))
	for _, model := range models {
		out = append(out, modelEntry{Model: model, Source: source})
	}
	return out
}

func staticMetaEntries() []modelEntry {
	return entries(provider.MetaModels(), "documented_catalog")
}

func mockModel() harness.ModelInfo {
	return harness.ModelInfo{
		ID: "mock-science-v1", Provider: "mock", DisplayName: "Deterministic Science Mock",
		Capabilities: harness.Capabilities{ToolCalls: true, StructuredOutput: true, Reasoning: true},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
