package storage

import (
	"path/filepath"
	"strings"
)

type Tree[T any] struct {
	Name     string     `json:"name"`
	Children []*Tree[T] `json:"children,omitempty"`
	Value    T          `json:"value,omitempty"`

	FullPath string `json:"full_path,omitempty"`
}

func NewTree[T any]() *Tree[T] {
	return &Tree[T]{}
}

func (p *Tree[T]) AddNode(name string, value T) {
	parts := strings.Split(filepath.Clean(name), string(filepath.Separator))

	current := p

	for index, part := range parts {
		var child *Tree[T]

		if len(current.Children) > 0 && current.Children[len(current.Children)-1].Name == part {
			child = current.Children[len(current.Children)-1]
		}

		if child == nil {
			child = &Tree[T]{
				Name:     part,
				FullPath: "/" + filepath.Join(parts[:index+1]...),
			}
			current.Children = append(current.Children, child)
		}

		current = child
	}

	current.Value = value
}

func (p *Tree[T]) Flatten() {
	for _, child := range p.Children {
		child.Flatten()
	}

	if p.HasSingleChild() {
		child := p.Children[0]
		p.Name = filepath.Join(p.Name, child.Name)
		p.Value = child.Value
		p.Children = child.Children
		p.FullPath = child.FullPath
		p.Flatten()
	}
}

// IsLeaf determines if this path is a leaf node (has no children).
func (p *Tree[T]) IsLeaf() bool {
	return len(p.Children) == 0
}

// IsGroup determines if this path is a group (has children).
func (p *Tree[T]) IsGroup() bool {
	return len(p.Children) > 0
}

// HasSingleChild checks if this path has exactly one child.
func (p *Tree[T]) HasSingleChild() bool {
	return len(p.Children) == 1
}
