package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/labstack/echo/v5"
	"github.com/onsi/gomega"
)

// buildMultipart builds a multipart body with the given field order.
// Each field is (name, contents) — contents for "workdir" is a zstd-compressed tar stub.
func buildMultipart(t *testing.T, fields []struct{ name, contents string }) (*bytes.Buffer, string) {
	t.Helper()

	var body bytes.Buffer

	w := multipart.NewWriter(&body)

	for _, f := range fields {
		part, err := w.CreateFormField(f.name)
		if err != nil {
			t.Fatal(err)
		}

		if f.name == "workdir" {
			// Wrap the string in a zstd stream.
			enc, encErr := zstd.NewWriter(part)
			if encErr != nil {
				t.Fatal(encErr)
			}

			_, _ = enc.Write([]byte(f.contents))
			_ = enc.Close()
		} else {
			_, _ = part.Write([]byte(f.contents))
		}
	}

	_ = w.Close()

	return &body, w.FormDataContentType()
}

func TestParseRunInput_UnknownPartIsSkipped(t *testing.T) {
	t.Parallel()

	assert := gomega.NewGomegaWithT(t)

	argsJSON, _ := json.Marshal([]string{"a"})

	body, contentType := buildMultipart(t, []struct{ name, contents string }{
		{"junk", "should-be-closed-not-leaked"},
		{"args", string(argsJSON)},
		{"workdir", "wd"},
	})

	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)

	args, workdirTar := parseRunInput(ctx)

	assert.Expect(args).To(gomega.Equal([]string{"a"}))
	assert.Expect(workdirTar).NotTo(gomega.BeNil())
	assert.Expect(workdirTar.Close()).To(gomega.Succeed())
}

func TestParseRunInput_ArgsFirstParsesWorkdir(t *testing.T) {
	t.Parallel()

	assert := gomega.NewGomegaWithT(t)

	argsJSON, _ := json.Marshal([]string{"x"})

	body, contentType := buildMultipart(t, []struct{ name, contents string }{
		{"args", string(argsJSON)},
		{"workdir", "tar-bytes"},
	})

	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)

	args, workdirTar := parseRunInput(ctx)

	assert.Expect(args).To(gomega.Equal([]string{"x"}))
	assert.Expect(workdirTar).NotTo(gomega.BeNil())

	data, err := io.ReadAll(workdirTar)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(string(data)).To(gomega.Equal("tar-bytes"))
	assert.Expect(workdirTar.Close()).To(gomega.Succeed())
}

func TestParseRunInput_ArgsOnly(t *testing.T) {
	t.Parallel()

	assert := gomega.NewGomegaWithT(t)

	argsJSON, _ := json.Marshal([]string{"only"})

	body, contentType := buildMultipart(t, []struct{ name, contents string }{
		{"args", string(argsJSON)},
	})

	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)

	args, workdirTar := parseRunInput(ctx)

	assert.Expect(args).To(gomega.Equal([]string{"only"}))
	assert.Expect(workdirTar).To(gomega.BeNil())
}
