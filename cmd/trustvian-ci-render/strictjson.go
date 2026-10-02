package main

// A strict JSON reader for untrusted artifact files.
//
// encoding/json's Unmarshal accepts a duplicate key (the last one wins), cannot
// tell an absent field from a null or a zero value once decoded into a struct,
// and stops at the first value without saying whether anything follows. Each
// of those would let a document mean two things at once, so the artifact is
// parsed here into a small tree first — every duplicate key, trailing value
// and invalid byte refused — and the schema is then checked field by field
// against that tree, where absent, null, zero and false stay distinct.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

// maxJSONDepth bounds nesting. The deepest field the renderer reads is six
// levels down in a suite member's embedded result; anything far deeper is not
// a document this renderer can consume.
const maxJSONDepth = 32

// maxJSONNodes bounds how many values one document may hold. The byte bound
// alone does not bound memory: 32 MiB of "[1,1,1,…" is sixteen million values,
// and each is a tree node. A real 32 MiB suite holds well under a million; two
// million keeps the tree to a few hundred megabytes at worst.
const maxJSONNodes = 2_000_000

type nodeKind int

const (
	kindNull nodeKind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

func (k nodeKind) String() string {
	return [...]string{"null", "a boolean", "a number", "a string", "an array", "an object"}[k]
}

// node is one parsed JSON value.
type node struct {
	kind    nodeKind
	boolean bool
	number  json.Number
	text    string
	items   []*node
	fields  map[string]*node
}

// errMalformed is wrapped by every parse failure. Its messages name byte
// offsets, never a key or value from the document.
var errMalformed = errors.New("malformed JSON")

// parseStrict parses exactly one JSON value from raw.
func parseStrict(raw []byte) (*node, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("%w: invalid UTF-8", errMalformed)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	nodes := 0
	root, err := parseValue(dec, 0, &nodes)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: data after the document, at byte %d", errMalformed, dec.InputOffset())
	}
	return root, nil
}

func parseValue(dec *json.Decoder, depth int, nodes *int) (*node, error) {
	if depth > maxJSONDepth {
		return nil, fmt.Errorf("%w: nested deeper than %d levels", errMalformed, maxJSONDepth)
	}
	if *nodes++; *nodes > maxJSONNodes {
		return nil, fmt.Errorf("%w: more than %d values", errMalformed, maxJSONNodes)
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: at byte %d", errMalformed, dec.InputOffset())
	}
	switch t := tok.(type) {
	case nil:
		return &node{kind: kindNull}, nil
	case bool:
		return &node{kind: kindBool, boolean: t}, nil
	case json.Number:
		return &node{kind: kindNumber, number: t}, nil
	case string:
		return &node{kind: kindString, text: t}, nil
	case json.Delim:
		switch t {
		case '[':
			n := &node{kind: kindArray}
			for dec.More() {
				item, err := parseValue(dec, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				n.items = append(n.items, item)
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("%w: at byte %d", errMalformed, dec.InputOffset())
			}
			return n, nil
		case '{':
			n := &node{kind: kindObject, fields: map[string]*node{}}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("%w: at byte %d", errMalformed, dec.InputOffset())
				}
				key, _ := keyTok.(string)
				if _, dup := n.fields[key]; dup {
					return nil, fmt.Errorf("%w: a duplicate object key, at byte %d", errMalformed, dec.InputOffset())
				}
				value, err := parseValue(dec, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				n.fields[key] = value
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("%w: at byte %d", errMalformed, dec.InputOffset())
			}
			return n, nil
		}
	}
	return nil, fmt.Errorf("%w: at byte %d", errMalformed, dec.InputOffset())
}

// errSchema is wrapped by every schema failure. Its messages name the path of
// a field this renderer defines — never an unknown key, and never a value.
var errSchema = errors.New("schema violation")

func schemaErr(path, problem string) error {
	return fmt.Errorf("%w: %s: %s", errSchema, path, problem)
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func index(path string, i int) string { return fmt.Sprintf("%s[%d]", path, i) }

// field returns a required field of an object, refusing an absent or null
// one and any other kind than want.
func field(obj *node, path, name string, want nodeKind) (*node, error) {
	n, ok := obj.fields[name]
	switch {
	case !ok:
		return nil, schemaErr(join(path, name), "required field is absent")
	case n.kind == kindNull:
		return nil, schemaErr(join(path, name), "required field is null")
	case n.kind != want:
		return nil, schemaErr(join(path, name), fmt.Sprintf("is %s, want %s", n.kind, want))
	}
	return n, nil
}

// optional returns an optional field, or nil when it is absent. A field that
// is present must have the wanted kind: null is not a way to omit it.
func optional(obj *node, path, name string, want nodeKind) (*node, error) {
	if _, ok := obj.fields[name]; !ok {
		return nil, nil
	}
	return field(obj, path, name, want)
}

func str(obj *node, path, name string) (string, error) {
	n, err := field(obj, path, name, kindString)
	if err != nil {
		return "", err
	}
	return n.text, nil
}

// nonEmpty is a required string that must hold something.
func nonEmpty(obj *node, path, name string) (string, error) {
	s, err := str(obj, path, name)
	if err == nil && s == "" {
		err = schemaErr(join(path, name), "is empty")
	}
	return s, err
}

func optionalStr(obj *node, path, name string) (string, bool, error) {
	n, err := optional(obj, path, name, kindString)
	if err != nil || n == nil {
		return "", false, err
	}
	return n.text, true, nil
}

func boolean(obj *node, path, name string) (bool, error) {
	n, err := field(obj, path, name, kindBool)
	if err != nil {
		return false, err
	}
	return n.boolean, nil
}

var canonicalInt = regexp.MustCompile(`^(0|-?[1-9][0-9]{0,17})$`)

// integer reads a required whole JSON number within [lo, hi]. 2.0, 1e3 and -0
// are refused: the producer writes integers in their canonical form.
func integer(obj *node, path, name string, lo, hi int64) (int64, error) {
	n, err := field(obj, path, name, kindNumber)
	if err != nil {
		return 0, err
	}
	return intValue(n, join(path, name), lo, hi)
}

func intValue(n *node, path string, lo, hi int64) (int64, error) {
	if !canonicalInt.MatchString(string(n.number)) {
		return 0, schemaErr(path, "is not a whole number")
	}
	v, err := n.number.Int64()
	if err != nil || v < lo || v > hi {
		return 0, schemaErr(path, fmt.Sprintf("is outside %d to %d", lo, hi))
	}
	return v, nil
}

// decimalText is a canonical unsigned decimal string, the form every count
// and limit takes on /v1. It is checked for shape and transcribed as text;
// the renderer never turns it into a number.
var decimalText = regexp.MustCompile(`^(0|[1-9][0-9]{0,19})$`)

func decimal(obj *node, path, name string) (string, error) {
	s, err := str(obj, path, name)
	if err == nil && !decimalText.MatchString(s) {
		err = schemaErr(join(path, name), "is not a canonical decimal string")
	}
	return s, err
}

// enum reads a required string from a closed vocabulary.
func enum(obj *node, path, name string, allowed ...string) (string, error) {
	s, err := str(obj, path, name)
	if err != nil {
		return "", err
	}
	for _, a := range allowed {
		if s == a {
			return s, nil
		}
	}
	return "", schemaErr(join(path, name), "is not one of its defined values")
}

func object(obj *node, path, name string) (*node, error) {
	return field(obj, path, name, kindObject)
}

func array(obj *node, path, name string) (*node, error) {
	return field(obj, path, name, kindArray)
}

// equalText requires a field to carry exactly want.
func equalText(obj *node, path, name, want string) error {
	s, err := str(obj, path, name)
	if err == nil && s != want {
		err = schemaErr(join(path, name), "is not a supported value")
	}
	return err
}
