package resources

import (
	"fmt"
	"sort"
)

// Registry holds a set of native resource implementations, keyed by name.
// Construct one with NewRegistry and pass it wherever native resources are needed.
type Registry struct {
	byName map[string]Resource
}

// NewRegistry builds a Registry from an explicit list of Resource values.
// Each resource is indexed by its Name() return value. Panics on duplicate names.
func NewRegistry(list []Resource) *Registry {
	m := make(map[string]Resource, len(list))

	for _, r := range list {
		name := r.Name()
		if _, exists := m[name]; exists {
			panic(fmt.Sprintf("resource type %q already registered", name))
		}

		m[name] = r
	}

	return &Registry{byName: m}
}

// Get returns the resource for the given name.
// Returns an error if the resource type is not registered.
func (r *Registry) Get(name string) (Resource, error) {
	res, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("unknown resource type: %s", name)
	}

	return res, nil
}

// IsNative returns true if a resource type is registered.
func (r *Registry) IsNative(name string) bool {
	_, ok := r.byName[name]

	return ok
}

// List returns a sorted list of all registered resource type names.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}
