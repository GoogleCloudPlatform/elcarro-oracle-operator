// Package ns provides a way to map entities from one namespace into another.
// The key idea here is that we can reduce the operator's privilege by only
// granting it privileges in a separate namespace which is not the same as where
// the operator's custom resources are created.
package ns

import (
	"hash/fnv"
)

// NamespaceMapper maps an entity namespace/name to another namespace/name.
type NamespaceMapper interface {
	// DestName returns the destination name of a given namespace/name.
	DestName(srcNS, srcName string) string

	// DestNamespace returns the destination namespace for a given namespace.
	DestNamespace(srcNS string) string
}

// NewSameMapper returns a NamespaceMapper that maps to the same namespace/name.
func NewSameMapper() NamespaceMapper {
	return &same{}
}

// NewRedirectMapper returns a NamespaceMapper that redirects all entities to
// one select target namespace.
// To avoid collision of the same name from different namespaces, the mapper
// prefixes the source-namespace and appends a hash.
// Long names may get truncated.
// Examples:
// destNs: "target"
// { ns: "a", "name: "b" } -> { ns: "target", name: "a-b-hash" }
// { ns: "", name: "c" } -> { ns: "target", name: "c-hash" }
func NewRedirectMapper(destNS string) NamespaceMapper {
	return &redirectToAnother{TargetNS: destNS}
}

// NewNSPrefixMapper returns a NamespaceMapper that redirects all entities to
// another namespace with the specified prefix. The entity name doesn't change.
// The destination namespace may have a prefix-hash appended at the end for
// long names (with truncation).
// Examples, for namespace prefix "pre"
// { ns: "a", "name: "b" } -> { ns: "pre-a", name: "b" }
// { ns: "", name: "c" } -> { ns: "pre", name: "c" }
// { ns: "long-name", name: "c" } -> { ns: "pre-long-clipped-hash", name: "c" }
func NewNSPrefixMapper(nsPrefix string) NamespaceMapper {
	return &prefixer{prefix: nsPrefix}
}

type same struct{}

func (*same) DestName(srcNS, srcName string) string {
	return srcName
}

func (*same) DestNamespace(srcNS string) string {
	return srcNS
}

type prefixer struct {
	prefix string
}

func (r *prefixer) DestName(srcNS, srcName string) string {
	return srcName
}

func (r *prefixer) DestNamespace(srcNS string) string {
	return munge(r.prefix, srcNS, 63, false)
}

type redirectToAnother struct {
	TargetNS string
}

func (r *redirectToAnother) DestName(srcNS, srcName string) string {
	return munge(srcNS, srcName, 63, true)
}

func (r *redirectToAnother) DestNamespace(srcNS string) string {
	return r.TargetNS
}

func munge(s1, s2 string, limit int, alwaysAddHash bool) string {
	j := s1 + "-" + s2
	if len(s1) == 0 {
		j = s2
	}
	if len(s2) == 0 {
		j = s1
	}
	hashRequired := alwaysAddHash
	if len(j) > limit-suffixLen {
		j = j[:limit-suffixLen]
		hashRequired = true
	}
	if hashRequired {
		j = j + hashSuffix(s1+"/"+s2)
	}
	return j
}

var charset = []rune("01234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

const suffixLen = 12

func hashSuffix(s string) string {
	e := fnv.New64()
	e.Write([]byte(s))
	i := e.Sum64()
	c := []rune{'-'}
	l := uint64(len(charset))
	for i > 0 {
		c = append(c, charset[i%l])
		i /= l
	}
	return string(c)
}
