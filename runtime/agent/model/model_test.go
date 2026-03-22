package model_test

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/runtime/agent/model"
)

func TestSplitModel(t *testing.T) {
	t.Parallel()

	t.Run("provider/model splits correctly", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		provider, name := model.SplitModel("openrouter/google/gemini-3")
		assert.Expect(provider).To(Equal("openrouter"))
		assert.Expect(name).To(Equal("google/gemini-3"))
	})

	t.Run("simple provider/model", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		provider, name := model.SplitModel("anthropic/claude-4")
		assert.Expect(provider).To(Equal("anthropic"))
		assert.Expect(name).To(Equal("claude-4"))
	})

	t.Run("no slash returns same string for both", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		provider, name := model.SplitModel("gpt-4")
		assert.Expect(provider).To(Equal("gpt-4"))
		assert.Expect(name).To(Equal("gpt-4"))
	})
}

func TestResolve_UnknownProvider(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)
	_, err := model.Resolve("unknown-provider", "model-x", "key", nil, nil)
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("unknown provider"))
}

func TestBuildGenerateContentConfig(t *testing.T) {
	t.Parallel()

	t.Run("nil when no tuning", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		cfg := model.BuildGenerateContentConfig("openai", nil, nil, nil)
		assert.Expect(cfg).To(BeNil())
	})

	t.Run("sets temperature", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		temp := float32(0.5)
		cfg := model.BuildGenerateContentConfig("openai", &model.LLMConfig{Temperature: &temp}, nil, nil)
		assert.Expect(cfg).NotTo(BeNil())
		assert.Expect(*cfg.Temperature).To(Equal(float32(0.5)))
	})

	t.Run("sets max tokens", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		cfg := model.BuildGenerateContentConfig("openai", &model.LLMConfig{MaxTokens: 1024}, nil, nil)
		assert.Expect(cfg).NotTo(BeNil())
		assert.Expect(cfg.MaxOutputTokens).To(Equal(int32(1024)))
	})
}
