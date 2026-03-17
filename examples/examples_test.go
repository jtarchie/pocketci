package main_test

import (
	"path/filepath"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/jtarchie/pocketci/orchestra/k8s"
	_ "github.com/jtarchie/pocketci/storage/sqlite"
	"github.com/jtarchie/pocketci/testhelpers"
	. "github.com/onsi/gomega"
)

func TestExamplesDocker(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	matches, err := doublestar.FilepathGlob("docker/*.{js,ts,yml,yaml}")
	assert.Expect(err).NotTo(HaveOccurred())

	// Check if k8s is available
	drivers := []string{"docker"}
	if k8s.IsAvailable() {
		drivers = append(drivers, "k8s")
	}

	for _, match := range matches {
		examplePath, err := filepath.Abs(match)
		assert.Expect(err).NotTo(HaveOccurred())

		for _, driver := range drivers {
			t.Run(driver+": "+match, func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				runner := testhelpers.Runner{
					Pipeline: examplePath,
					Driver:   driver,
					Storage:  "sqlite://:memory:",
				}
				err := runner.Run(nil)
				assert.Expect(err).NotTo(HaveOccurred())
			})
		}
	}
}

func TestExamplesAll(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	matches, err := doublestar.FilepathGlob("both/*.{js,ts,yml,yaml}")
	assert.Expect(err).NotTo(HaveOccurred())

	drivers := []string{
		"docker",
		"native",
	}

	for _, match := range matches {
		examplePath, err := filepath.Abs(match)
		assert.Expect(err).NotTo(HaveOccurred())

		for _, driver := range drivers {
			t.Run(driver+": "+match, func(t *testing.T) {
				t.Parallel()

				assert := NewGomegaWithT(t)
				runner := testhelpers.Runner{
					Pipeline: examplePath,
					Driver:   driver,
					Storage:  "sqlite://:memory:",
				}
				err := runner.Run(nil)
				assert.Expect(err).NotTo(HaveOccurred())
			})
		}
	}
}
