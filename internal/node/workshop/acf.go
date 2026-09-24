// SPDX-License-Identifier: GPL-3.0-or-later

// Package workshop keeps one copy of each CS2 workshop item on the node and
// hands it to every server as a link into a read-only mount, so a map costs
// its disk once instead of once per server.
package workshop

import (
	"errors"
	"fmt"
	"strings"
)

// KV is a Valve KeyValues node: ordered pairs whose value is a string or a
// nested node. Only what appworkshop_730.acf uses is supported.
type KV struct {
	keys []string
	vals map[string]any
}

// NewKV is an empty node.
func NewKV() *KV { return &KV{vals: map[string]any{}} }

// Keys in file order.
func (k *KV) Keys() []string { return append([]string(nil), k.keys...) }

// Str reads a leaf value, "" when missing or nested.
func (k *KV) Str(key string) string {
	if k == nil {
		return ""
	}
	s, _ := k.vals[key].(string)
	return s
}

// Sub reads a nested node, nil when missing or a leaf.
func (k *KV) Sub(key string) *KV {
	if k == nil {
		return nil
	}
	s, _ := k.vals[key].(*KV)
	return s
}

// SetStr writes a leaf, keeping the position of an existing key.
func (k *KV) SetStr(key, val string) { k.set(key, val) }

// Ensure returns the nested node, creating it at the end when missing.
func (k *KV) Ensure(key string) *KV {
	if s := k.Sub(key); s != nil {
		return s
	}
	s := NewKV()
	k.set(key, s)
	return s
}

// Delete removes a key.
func (k *KV) Delete(key string) {
	if _, ok := k.vals[key]; !ok {
		return
	}
	delete(k.vals, key)
	for i, existing := range k.keys {
		if existing == key {
			k.keys = append(k.keys[:i], k.keys[i+1:]...)
			break
		}
	}
}

func (k *KV) set(key string, val any) {
	if _, ok := k.vals[key]; !ok {
		k.keys = append(k.keys, key)
	}
	k.vals[key] = val
}

// ParseACF reads a KeyValues document. The outer name (AppWorkshop) is kept
// as the single key of the returned node.
func ParseACF(text string) (*KV, error) {
	p := &parser{src: text}
	root := NewKV()
	for {
		p.skipSpace()
		if p.done() {
			return root, nil
		}
		key, err := p.token()
		if err != nil {
			return nil, err
		}
		val, err := p.value()
		if err != nil {
			return nil, err
		}
		root.set(key, val)
	}
}

type parser struct {
	src   string
	pos   int
	depth int
}

func (p *parser) done() bool { return p.pos >= len(p.src) }

func (p *parser) skipSpace() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		case '/': // // comment
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

// token reads one quoted string.
func (p *parser) token() (string, error) {
	if p.done() || p.src[p.pos] != '"' {
		return "", fmt.Errorf("expected a quoted key at byte %d", p.pos)
	}
	p.pos++
	var b strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch c {
		case '\\':
			if p.pos+1 < len(p.src) {
				p.pos++
				b.WriteByte(p.src[p.pos])
			}
		case '"':
			p.pos++
			return b.String(), nil
		default:
			b.WriteByte(c)
		}
		p.pos++
	}
	return "", errors.New("unterminated string")
}

// MaxDepth bounds how deep a document may nest. The file is written by the
// game inside a container the server's users control, and every "{" here is
// one Go stack frame: a few megabytes of "a{a{a{..." would otherwise grow
// the stack past its 1 GB ceiling, which is a fatal runtime error the
// daemon cannot recover from.
const MaxDepth = 64

// value reads either a quoted leaf or a braced node.
func (p *parser) value() (any, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > MaxDepth {
		return nil, fmt.Errorf("nested deeper than %d levels", MaxDepth)
	}
	p.skipSpace()
	if p.done() {
		return nil, errors.New("value expected")
	}
	if p.src[p.pos] == '"' {
		return p.token()
	}
	if p.src[p.pos] != '{' {
		return nil, fmt.Errorf("expected a value at byte %d", p.pos)
	}
	p.pos++
	node := NewKV()
	for {
		p.skipSpace()
		if p.done() {
			return nil, errors.New("unterminated block")
		}
		if p.src[p.pos] == '}' {
			p.pos++
			return node, nil
		}
		key, err := p.token()
		if err != nil {
			return nil, err
		}
		val, err := p.value()
		if err != nil {
			return nil, err
		}
		node.set(key, val)
	}
}

// String renders the document the way Steam writes it: tabs, quoted keys,
// values aligned on their own column.
func (k *KV) String() string {
	var b strings.Builder
	k.write(&b, 0)
	return b.String()
}

func (k *KV) write(b *strings.Builder, depth int) {
	pad := strings.Repeat("\t", depth)
	for _, key := range k.keys {
		switch v := k.vals[key].(type) {
		case string:
			fmt.Fprintf(b, "%s%q\t\t%q\n", pad, key, v)
		case *KV:
			fmt.Fprintf(b, "%s%q\n%s{\n", pad, key, pad)
			v.write(b, depth+1)
			fmt.Fprintf(b, "%s}\n", pad)
		}
	}
}
