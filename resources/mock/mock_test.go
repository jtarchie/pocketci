package mock_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jtarchie/pocketci/resources"
	_ "github.com/jtarchie/pocketci/resources/mock"
	. "github.com/onsi/gomega"
)

func TestMockResource(t *testing.T) {
	t.Parallel()
	t.Run("is registered", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		assert.Expect(resources.IsNative("mock")).To(BeTrue())

		res, err := resources.Get("mock")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(res.Name()).To(Equal("mock"))
	})

	t.Run("check returns force_version when specified", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		res, err := resources.Get("mock")
		assert.Expect(err).NotTo(HaveOccurred())

		ctx := context.Background()
		resp, err := res.Check(ctx, resources.CheckRequest{
			Source: map[string]any{
				"force_version": "test-version",
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(resp).To(HaveLen(1))
		assert.Expect(resp[0]["version"]).To(Equal("test-version"))
	})

	t.Run("in creates version file", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		res, err := resources.Get("mock")
		assert.Expect(err).NotTo(HaveOccurred())

		vol := &resources.DirVolumeContext{Dir: t.TempDir()}

		ctx := context.Background()
		inResp, err := res.In(ctx, vol, resources.InRequest{
			Source: map[string]any{},
			Version: resources.Version{
				"version": "1.0.0",
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(inResp.Version["version"]).To(Equal("1.0.0"))

		// Verify version file was created
		content, err := os.ReadFile(filepath.Join(vol.Dir, "version"))
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(string(content)).To(Equal("1.0.0"))
	})

	t.Run("out returns version from params", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		res, err := resources.Get("mock")
		assert.Expect(err).NotTo(HaveOccurred())

		ctx := context.Background()
		outResp, err := res.Out(ctx, nil, resources.OutRequest{
			Source: map[string]any{},
			Params: map[string]any{
				"version": "2.0.0",
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(outResp.Version["version"]).To(Equal("2.0.0"))
	})

	t.Run("check with no version returns version 1", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		res, err := resources.Get("mock")
		assert.Expect(err).NotTo(HaveOccurred())

		ctx := context.Background()
		resp, err := res.Check(ctx, resources.CheckRequest{
			Source: map[string]any{},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(resp).To(HaveLen(1))
		assert.Expect(resp[0]["version"]).To(Equal("1"))
	})

	t.Run("check with previous version increments", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		res, err := resources.Get("mock")
		assert.Expect(err).NotTo(HaveOccurred())

		ctx := context.Background()
		resp, err := res.Check(ctx, resources.CheckRequest{
			Source:  map[string]any{},
			Version: resources.Version{"version": "5"},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(resp).To(HaveLen(2))
		assert.Expect(resp[0]["version"]).To(Equal("5"))
		assert.Expect(resp[1]["version"]).To(Equal("6"))
	})

	t.Run("check with force_version and previous version returns both", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		res, err := resources.Get("mock")
		assert.Expect(err).NotTo(HaveOccurred())

		ctx := context.Background()
		resp, err := res.Check(ctx, resources.CheckRequest{
			Source: map[string]any{
				"force_version": "fixed-v1",
			},
			Version: resources.Version{"version": "fixed-v1"},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(resp).To(HaveLen(2))
		assert.Expect(resp[0]["version"]).To(Equal("fixed-v1"))
		assert.Expect(resp[1]["version"]).To(Equal("fixed-v1"))
	})
}
