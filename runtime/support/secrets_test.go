package support_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/secrets"
	. "github.com/onsi/gomega"
)

type mapSecretsManager struct {
	data map[string]string
}

func (m *mapSecretsManager) Get(_ context.Context, scope, key string) (string, error) {
	if v, ok := m.data[scope+"/"+key]; ok {
		return v, nil
	}

	return "", secrets.ErrNotFound
}

func (m *mapSecretsManager) Set(_ context.Context, scope, key, value string) error {
	m.data[scope+"/"+key] = value
	return nil
}

func (m *mapSecretsManager) Delete(_ context.Context, scope, key string) error {
	delete(m.data, scope+"/"+key)
	return nil
}

func (m *mapSecretsManager) ListByScope(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (m *mapSecretsManager) DeleteByScope(_ context.Context, _ string) error {
	return nil
}

func (m *mapSecretsManager) Close() error { return nil }

func TestResolveSecretString_RejectsNULByte(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	mgr := &mapSecretsManager{data: map[string]string{
		string(secrets.GlobalScope) + "/TOKEN": "good\x00bad",
	}}

	_, _, err := support.ResolveSecretString(context.Background(), mgr, "pipeline-1", "secret:TOKEN")

	assert.Expect(err).To(MatchError(support.ErrSecretContainsNUL))
}

func TestResolveSecretString_PassesValidValue(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	mgr := &mapSecretsManager{data: map[string]string{
		string(secrets.GlobalScope) + "/TOKEN": "valid-value\nwith-newline",
	}}

	val, wasSecret, err := support.ResolveSecretString(context.Background(), mgr, "pipeline-1", "secret:TOKEN")

	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(wasSecret).To(BeTrue())
	assert.Expect(val).To(Equal("valid-value\nwith-newline"))
}

func TestResolveSecretString_PassThroughWhenNoPrefix(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	mgr := &mapSecretsManager{data: map[string]string{}}

	val, wasSecret, err := support.ResolveSecretString(context.Background(), mgr, "pipeline-1", "plain-value")

	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(wasSecret).To(BeFalse())
	assert.Expect(val).To(Equal("plain-value"))
}

func TestResolveSecretString_MissingSecret(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	mgr := &mapSecretsManager{data: map[string]string{}}

	_, _, err := support.ResolveSecretString(context.Background(), mgr, "pipeline-1", "secret:MISSING")

	assert.Expect(err).To(HaveOccurred())
	assert.Expect(errors.Is(err, secrets.ErrNotFound)).To(BeTrue())
}
