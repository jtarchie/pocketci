package http_test

import (
	"testing"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	nhttp "github.com/jtarchie/pocketci/runtime/jsapi/notifiers/http"
	"github.com/nikoksr/notify"
	. "github.com/onsi/gomega"
)

func TestHTTPMissingURL(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	err := nhttp.New().Configure(notify.New(), jsapi.NotifyConfig{})
	assert.Expect(err).To(MatchError(ContainSubstring("HTTP URL is required")))
}

func TestHTTPName(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)
	assert.Expect(nhttp.New().Name()).To(Equal("http"))
}
