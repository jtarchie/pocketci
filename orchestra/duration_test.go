package orchestra_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	. "github.com/onsi/gomega"
)

func TestDuration(t *testing.T) {
	t.Parallel()

	t.Run("MarshalJSON", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		d := orchestra.Duration(5 * time.Minute)

		b, err := json.Marshal(d)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(string(b)).To(Equal(`"5m0s"`))
	})

	t.Run("UnmarshalJSON from string", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		var d orchestra.Duration

		err := json.Unmarshal([]byte(`"10s"`), &d)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(d.Std()).To(Equal(10 * time.Second))
	})

	t.Run("UnmarshalJSON from number", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		ns := float64(3 * time.Second)

		b, err := json.Marshal(ns)
		assert.Expect(err).NotTo(HaveOccurred())

		var d orchestra.Duration

		err = json.Unmarshal(b, &d)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(d.Std()).To(Equal(3 * time.Second))
	})

	t.Run("roundtrip", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		original := orchestra.Duration(2*time.Hour + 30*time.Minute)

		b, err := json.Marshal(original)
		assert.Expect(err).NotTo(HaveOccurred())

		var decoded orchestra.Duration

		err = json.Unmarshal(b, &decoded)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(decoded).To(Equal(original))
	})

	t.Run("zero value", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		var d orchestra.Duration

		b, err := json.Marshal(d)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(string(b)).To(Equal(`"0s"`))

		var decoded orchestra.Duration

		err = json.Unmarshal(b, &decoded)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(decoded.Std()).To(Equal(time.Duration(0)))
	})

	t.Run("Std conversion", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		d := orchestra.Duration(42 * time.Millisecond)
		assert.Expect(d.Std()).To(Equal(42 * time.Millisecond))
	})

	t.Run("invalid string", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		var d orchestra.Duration

		err := json.Unmarshal([]byte(`"not-a-duration"`), &d)
		assert.Expect(err).To(HaveOccurred())
	})

	t.Run("invalid type", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		var d orchestra.Duration

		err := json.Unmarshal([]byte(`true`), &d)
		assert.Expect(err).To(HaveOccurred())
	})
}
