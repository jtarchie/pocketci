package agent

import (
	"context"
	"fmt"
	"strings"

	genaianthropic "github.com/achetronic/adk-utils-go/genai/anthropic"
	genaiopenai "github.com/achetronic/adk-utils-go/genai/openai"

	adkmodel "google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/jtarchie/pocketci/secrets"
)

// DefaultBaseURLs maps providers (that use the OpenAI-compatible API) to their base URLs.
var DefaultBaseURLs = map[string]string{
	"openrouter": "https://openrouter.ai/api/v1",
	"ollama":     "http://localhost:11434/v1",
	"openai":     "https://api.openai.com/v1",
}

// splitModel splits "provider/model-name" into ("provider", "model-name").
// For example: "openrouter/google/gemini-3" → ("openrouter", "google/gemini-3").
func splitModel(model string) (provider, modelName string) {
	idx := strings.Index(model, "/")
	if idx < 0 {
		return model, model
	}

	return model[:idx], model[idx+1:]
}

// resolveSecret looks up a secret key in pipeline → global scope order.
// Falls back to the corresponding environment variable (PROVIDER_API_KEY) if not found.
func resolveSecret(ctx context.Context, sm secrets.Manager, pipelineID, key string) string {
	if sm != nil {
		if pipelineID != "" {
			val, err := sm.Get(ctx, secrets.PipelineScope(pipelineID), key)
			if err == nil {
				return val
			}
		}

		val, err := sm.Get(ctx, secrets.GlobalScope, key)
		if err == nil {
			return val
		}
	}

	return ""
}

// resolveModel builds an adk-compatible LLM model from provider + name + key.
// llmCfg sets temperature and output token limit for all providers.
// thinkingCfg provides Anthropic-specific extended thinking budget.
func resolveModel(provider, modelName, apiKey string, llmCfg *AgentLLMConfig, thinkingCfg *AgentThinkingConfig) (adkmodel.LLM, error) {
	switch provider {
	case "anthropic":
		cfg := genaianthropic.Config{
			APIKey:    apiKey,
			ModelName: modelName,
		}

		if llmCfg != nil && llmCfg.MaxTokens > 0 {
			cfg.MaxOutputTokens = int(llmCfg.MaxTokens)
		}

		if thinkingCfg != nil && thinkingCfg.Budget > 0 {
			cfg.ThinkingBudgetTokens = int(thinkingCfg.Budget)
			// Anthropic requires MaxOutputTokens > ThinkingBudgetTokens.
			// Default to 8192 if not explicitly set.
			if cfg.MaxOutputTokens == 0 {
				cfg.MaxOutputTokens = 8192
			}
		}

		return genaianthropic.New(cfg), nil
	default:
		// openrouter, openai, ollama, etc. all speak OpenAI-compatible API.
		baseURL := DefaultBaseURLs[provider]
		if baseURL == "" {
			return nil, fmt.Errorf("unknown provider %q: set a base URL or use anthropic/openai/openrouter/ollama", provider)
		}

		return genaiopenai.New(genaiopenai.Config{
			APIKey:    apiKey,
			BaseURL:   baseURL,
			ModelName: modelName,
		}), nil
	}
}

// simpleRegistry is a fallback ModelRegistry for contextguard that returns
// conservative defaults when the model is not in a curated database.
type simpleRegistry struct{}

func (simpleRegistry) ContextWindow(_ string) int    { return 128000 }
func (simpleRegistry) DefaultMaxTokens(_ string) int { return 4096 }

// harmCategoryFromString maps a YAML harm category key to a genai.HarmCategory.
func harmCategoryFromString(s string) genai.HarmCategory {
	return genai.HarmCategory("HARM_CATEGORY_" + strings.ToUpper(s))
}

// harmThresholdFromString maps a YAML threshold value to a genai.HarmBlockThreshold.
func harmThresholdFromString(s string) genai.HarmBlockThreshold {
	upper := strings.ToUpper(s)
	// "off" → "OFF"; everything else needs the BLOCK_ prefix already present
	// in the canonical names (e.g. "block_none" → "BLOCK_NONE").
	switch upper {
	case "OFF":
		return genai.HarmBlockThreshold("OFF")
	default:
		return genai.HarmBlockThreshold(upper)
	}
}

// buildGenerateContentConfig constructs a genai.GenerateContentConfig from the
// agent config fields. Returns nil when no tuning is requested.
func buildGenerateContentConfig(provider string, llmCfg *AgentLLMConfig, thinkingCfg *AgentThinkingConfig, safety map[string]string) *genai.GenerateContentConfig {
	var gcc genai.GenerateContentConfig
	has := false

	if llmCfg != nil {
		if llmCfg.Temperature != nil {
			gcc.Temperature = llmCfg.Temperature
			has = true
		}

		if llmCfg.MaxTokens > 0 {
			gcc.MaxOutputTokens = llmCfg.MaxTokens
			has = true
		}
	}

	// For non-Anthropic providers, wire thinking via GenerateContentConfig.
	if thinkingCfg != nil && provider != "anthropic" {
		budget := thinkingCfg.Budget
		tc := &genai.ThinkingConfig{ThinkingBudget: &budget}

		if thinkingCfg.Level != "" {
			tc.ThinkingLevel = genai.ThinkingLevel(strings.ToUpper(thinkingCfg.Level))
		}

		gcc.ThinkingConfig = tc
		has = true
	}

	if len(safety) > 0 {
		for category, threshold := range safety {
			gcc.SafetySettings = append(gcc.SafetySettings, &genai.SafetySetting{
				Category:  harmCategoryFromString(category),
				Threshold: harmThresholdFromString(threshold),
			})
		}

		has = true
	}

	if !has {
		return nil
	}

	return &gcc
}
