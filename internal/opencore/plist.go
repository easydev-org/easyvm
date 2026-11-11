package opencore

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"os"
	"strconv"
	"strings"
	"time"
)

// xmlEscape protects the strings a config.plist contains - a comment may hold an ampersand, and
// an unescaped one makes the whole file unreadable to OpenCore.
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// --- crc32 and time, named so the GPT code reads as itself -----------------------------

// crc32IEEE is zlib's CRC32, which is what a GPT header carries.
func crc32IEEE(data []byte) uint32 { return crc32.ChecksumIEEE(data) }

func timeNowUnixNano() int64 { return time.Now().UnixNano() }

// --- Apple's XML property list --------------------------------------------------------
//
// THIS IS A REAL PARSE RATHER THAN A TEXT EDIT, and the Python implementation's own note says why
// better than a summary can:
//
//	The previous version edited the XML in place with a positional XPath:
//	`key[1]/following-sibling::*[1]/key[3]/...`. A plist dict is a flat list of key/value
//	pairs, so that indexing is correct only while every dict's key order is exactly what it
//	was when the expression was written. Reorder one dict upstream - which a release bump
//	does - and the edit lands on a neighbouring key and succeeds, reporting success. There is
//	nothing in the output that says which key was written.
//
// The subset here is what an OpenCore config.plist uses: dicts, arrays, strings, data, integers,
// reals and the two booleans. It keeps the file's own order, which is what makes the round trip
// byte-comparable in content.

type plistKind int

const (
	plistDict plistKind = iota
	plistArray
	plistString
	plistData
	plistInteger
	plistReal
	plistTrue
	plistFalse
)

type plistEntry struct {
	key   string
	value *plistValue
}

type plistValue struct {
	kind    plistKind
	str     string
	data    []byte
	integer int64
	real    float64
	dict    []plistEntry
	array   []*plistValue
}

func newDict() *plistValue           { return &plistValue{kind: plistDict} }
func newString(s string) *plistValue { return &plistValue{kind: plistString, str: s} }
func newInteger(v int64) *plistValue { return &plistValue{kind: plistInteger, integer: v} }

func newBool(b bool) *plistValue {
	if b {
		return &plistValue{kind: plistTrue}
	}
	return &plistValue{kind: plistFalse}
}

func (v *plistValue) get(key string) *plistValue {
	if v == nil || v.kind != plistDict {
		return nil
	}
	for _, e := range v.dict {
		if e.key == key {
			return e.value
		}
	}
	return nil
}

// set replaces a key's value in place, or appends it - which is what keeps the file's own order
// through a round trip.
func (v *plistValue) set(key string, value *plistValue) {
	for i, e := range v.dict {
		if e.key == key {
			v.dict[i].value = value
			return
		}
	}
	v.dict = append(v.dict, plistEntry{key: key, value: value})
}

// dig walks a path of keys, returning nil at the first missing step.
func (v *plistValue) dig(keys ...string) *plistValue {
	cur := v
	for _, k := range keys {
		if cur == nil {
			return nil
		}
		cur = cur.get(k)
	}
	return cur
}

// readPlist parses an XML property list.
func readPlist(path string) (*plistValue, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	// OpenCore writes XML, but a binary plist here would be a different format entirely and a
	// silent nil from the parser - so it is named rather than left to fail somewhere later.
	if strings.HasPrefix(string(body), "bplist") {
		return nil, fmt.Errorf("%s is a BINARY plist; this build reads and writes XML, and "+
			"converting it would lose the file's own key order", path)
	}

	var root *plistValue
	// A TOKEN WALK RATHER THAN STRUCT TAGS, because a plist's shape is its own: a dict is a flat
	// sequence of <key> then a value element, with no wrapper element per pair.
	stack := []*plistValue{}
	// EACH DICT NEEDS ITS OWN PENDING KEY. A single variable was the bug: `<dict><key>a</key>
	// <dict><key>b</key><string/></dict><key>c</key><string/></dict>` set the key for the inner
	// dict, then used it for the outer one.
	pendingKeys := []string{""}
	var lastText strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "plist":
				// The wrapper; its children are the real root.
			case "dict", "array", "string", "data", "integer", "real", "true", "false":
				val := &plistValue{}
				switch t.Name.Local {
				case "dict":
					val.kind = plistDict
				case "array":
					val.kind = plistArray
				case "string":
					val.kind = plistString
				case "data":
					val.kind = plistData
				case "integer":
					val.kind = plistInteger
				case "real":
					val.kind = plistReal
				case "true":
					val.kind = plistTrue
				case "false":
					val.kind = plistFalse
				}
				lastText.Reset()
				// Attach to the parent, which is either a dict (with pendingKey) or an array.
				if len(stack) == 0 {
					root = val
				} else {
					parent := stack[len(stack)-1]
					if parent.kind == plistDict {
						key := pendingKeys[len(pendingKeys)-1]
						parent.set(key, val)
						pendingKeys[len(pendingKeys)-1] = ""
					} else if parent.kind == plistArray {
						parent.array = append(parent.array, val)
					}
				}
				stack = append(stack, val)
				// A DICT PUSHES ITS OWN KEY SLOT; anything else does not.
				if val.kind == plistDict {
					pendingKeys = append(pendingKeys, "")
				}
			case "key":
				lastText.Reset()
			}
		case xml.CharData:
			lastText.Write(t)
		case xml.EndElement:
			switch t.Name.Local {
			case "key":
				if n := len(pendingKeys) - 1; n >= 0 {
					pendingKeys[n] = strings.TrimSpace(lastText.String())
				}
				lastText.Reset()
				continue
			case "string", "data", "integer", "real":
				// THE VALUE IS THE TEXT BETWEEN THE TAGS, so this branch reads it before popping.
				if len(stack) > 0 {
					cur := stack[len(stack)-1]
					text := lastText.String()
					switch cur.kind {
					case plistString:
						cur.str = text
					case plistData:
						// Apple writes base64, and the whitespace inside it is not data.
						compact := strings.Join(strings.Fields(text), "")
						if raw, err := base64.StdEncoding.DecodeString(compact); err == nil {
							cur.data = raw
						}
					case plistInteger:
						if n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil {
							cur.integer = n
						}
					case plistReal:
						if f, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil {
							cur.real = f
						}
					}
				}
			}
			// EVERY ELEMENT THAT WAS PUSHED IS POPPED, and that includes the scalars. Leaving
			// `string` on the stack was the bug that made every key after it attach to the
			// STRING instead of the dict: `<key>Resolution</key><string></string><key>Drivers</key>`
			// put Drivers inside Resolution, so a config with a drivers list parsed as a config with
			// one empty string and no drivers at all - and every check on this side passed.
			if len(stack) > 0 && t.Name.Local != "key" && t.Name.Local != "plist" {
				popped := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if popped.kind == plistDict && len(pendingKeys) > 1 {
					pendingKeys = pendingKeys[:len(pendingKeys)-1]
				}
			}
			lastText.Reset()
		}
	}
	if root == nil {
		return nil, fmt.Errorf("%s holds no plist value", path)
	}
	return root, nil
}

// writePlist writes an XML property list, keeping key order.
func writePlist(path string, root *plistValue) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" ` +
		`"http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	encodeValue(&b, root, 0)
	b.WriteString("</plist>\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func encodeValue(b *strings.Builder, v *plistValue, depth int) {
	pad := strings.Repeat("\t", depth)
	switch v.kind {
	case plistDict:
		if len(v.dict) == 0 {
			b.WriteString(pad + "<dict/>\n")
			return
		}
		b.WriteString(pad + "<dict>\n")
		for _, e := range v.dict {
			b.WriteString(pad + "\t<key>" + xmlEscape(e.key) + "</key>\n")
			encodeValue(b, e.value, depth+1)
		}
		b.WriteString(pad + "</dict>\n")
	case plistArray:
		if len(v.array) == 0 {
			b.WriteString(pad + "<array/>\n")
			return
		}
		b.WriteString(pad + "<array>\n")
		for _, item := range v.array {
			encodeValue(b, item, depth+1)
		}
		b.WriteString(pad + "</array>\n")
	case plistString:
		b.WriteString(pad + "<string>" + xmlEscape(v.str) + "</string>\n")
	case plistData:
		// APPLE WRITES <data> WITH ITS BASE64 BROKEN EVERY 68 CHARACTERS, and OpenCore is happy
		// with any whitespace. One line is simpler and the same bytes.
		b.WriteString(pad + "<data>" + base64.StdEncoding.EncodeToString(v.data) + "</data>\n")
	case plistInteger:
		b.WriteString(pad + "<integer>" + strconv.FormatInt(v.integer, 10) + "</integer>\n")
	case plistReal:
		b.WriteString(pad + "<real>" + strconv.FormatFloat(v.real, 'g', -1, 64) + "</real>\n")
	case plistTrue:
		b.WriteString(pad + "<true/>\n")
	case plistFalse:
		b.WriteString(pad + "<false/>\n")
	}
}
