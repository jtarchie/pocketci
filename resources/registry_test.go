package resources_test

import (
	"testing"

	"github.com/jtarchie/pocketci/resources"
	"github.com/jtarchie/pocketci/resources/mock"
	. "github.com/onsi/gomega"
)

func TestRegistry(t *testing.T) {
	t.Parallel()

	registry := resources.NewRegistry([]resources.Resource{
		&mock.Mock{},
	})

	t.Run("List returns registered resources", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		list := registry.List()
		assert.Expect(list).To(ContainElement("mock"))
	})

	t.Run("Get returns error for unknown resource", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		_, err := registry.Get("nonexistent-resource-type")
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("unknown resource type"))
	})

	t.Run("IsNative returns true for registered resource", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		assert.Expect(registry.IsNative("mock")).To(BeTrue())
	})

	t.Run("IsNative returns false for unknown resource", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		assert.Expect(registry.IsNative("nonexistent")).To(BeFalse())
	})

	t.Run("Get returns a valid resource", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		mockResource, err := registry.Get("mock")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(mockResource).NotTo(BeNil())
		assert.Expect(mockResource.Name()).To(Equal("mock"))
	})
}
