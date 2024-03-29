// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package ucd provides a parser for Unicode Character Database files, the
// format of which is defined in http://www.unicode.org/reports/tr44/. See
// http://www.unicode.org/Public/UCD/latest/ucd/ for example files.
//
// It currently does not support substitutions of missing fields.
package ucd

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const (
	// UnicodeData.txt fields.
	CodePoint = iota
	Name
	GeneralCategory
	CanonicalCombiningClass
	BidiClass
	DecompMapping
	DecimalValue
	DigitValue
	NumericValue
	BidiMirrored
	Unicode1Name
	ISOComment
	SimpleUppercaseMapping
	SimpleLowercaseMapping
	SimpleTitlecaseMapping
)

// An Option is used to configure a Parser.
type Option func(p *Parser)

func keepRanges(p *Parser) {
	p.keepRanges = true
}

var (
	// KeepRanges prevents the expansion of ranges. The raw ranges can be
	// obtained by calling Range(0) on the parser.
	KeepRanges Option = keepRanges
)

// A Parser parses Unicode Character Database (UCD) files.
type Parser struct {
	scanner *bufio.Scanner

	keepRanges bool // Don't expand rune ranges in field 0.

	err     error
	comment []byte
	field   [][]byte
	// parsedRange is needed in case Range(0) is called more than once for one
	// field. In some cases this requires scanning ahead.
	parsedRange          bool
	rangeStart, rangeEnd rune
}

func (p *Parser) setError(err error) {
	if p.err == nil {
		p.err = err
	}
}

func (p *Parser) getField(i int) []byte {
	if i >= len(p.field) {
		p.setError(fmt.Errorf("ucd: index of field %d out of bounds", i))
		return nil
	}
	return p.field[i]
}

// Err returns a non-nil error if any error occurred during parsing.
func (p *Parser) Err() error {
	return p.err
}

// New returns a Parser for the given Reader.
func New(r io.Reader, o ...Option) *Parser {
	p := &Parser{
		scanner: bufio.NewScanner(r),
	}
	for _, f := range o {
		f(p)
	}
	return p
}

// Next parses the next line in the file. It returns true if a line was parsed
// and false if it reached the end of the file.
func (p *Parser) Next() bool {
	if !p.keepRanges && p.rangeStart < p.rangeEnd {
		p.rangeStart++
		return true
	}
	p.comment = nil
	p.field = p.field[:0]
	p.parsedRange = false

	for p.scanner.Scan() {
		b := p.scanner.Bytes()
		if len(b) == 0 || b[0] == '#' {
			continue
		}

		// Parse line
		if i := bytes.IndexByte(b, '#'); i != -1 {
			p.comment = bytes.TrimSpace(b[i+1:])
			b = b[:i]
		}
		if b[0] == '@' {
			// We skip directives for now.
			continue
		}
		for {
			i := bytes.IndexByte(b, ';')
			if i == -1 {
				p.field = append(p.field, bytes.TrimSpace(b))
				break
			}
			p.field = append(p.field, bytes.TrimSpace(b[:i]))
			b = b[i+1:]
		}
		if !p.keepRanges {
			p.rangeStart, p.rangeEnd = p.getRange(0)
		}
		return true
	}
	p.setError(p.scanner.Err())
	return false
}

func (p *Parser) parseRune(b []byte) rune {
	if len(b) > 2 && b[0] == 'U' && b[1] == '+' {
		b = b[2:]
	}
	x, err := strconv.ParseUint(string(b), 16, 32)
	p.setError(err)
	return rune(x)
}

// Rune parses and returns field i as a rune.
func (p *Parser) Rune(i int) rune {
	if i > 0 || p.keepRanges {
		return p.parseRune(p.getField(i))
	}
	return p.rangeStart
}

// Runes interprets and returns field i as a sequence of runes.
func (p *Parser) Runes(i int) (runes []rune) {
	add := func(b []byte) {
		if b = bytes.TrimSpace(b); len(b) > 0 {
			runes = append(runes, p.parseRune(b))
		}
	}
	for b := p.getField(i); ; {
		i := bytes.IndexByte(b, ' ')
		if i == -1 {
			add(b)
			break
		}
		add(b[:i])
		b = b[i+1:]
	}
	return
}

var (
	errIncorrectLegacyRange = errors.New("ucd: unmatched <* First>")

	// reRange matches one line of a legacy rune range.
	reRange = regexp.MustCompile("^([0-9A-F]*);<([^,]*), ([^>]*)>(.*)$")
)

// Range parses and returns field i as a rune range. A range is inclusive at
// both ends. If the field only has one rune, first and last will be identical.
// It supports the legacy format for ranges used in UnicodeData.txt.
func (p *Parser) Range(i int) (first, last rune) {
	if !p.keepRanges {
		return p.rangeStart, p.rangeStart
	}
	return p.getRange(i)
}

func (p *Parser) getRange(i int) (first, last rune) {
	b := p.getField(i)
	if k := bytes.Index(b, []byte("..")); k != -1 {
		return p.parseRune(b[:k]), p.parseRune(b[k+2:])
	}
	x := p.parseRune(b)
	// Special case for UnicodeData that was retained for backwards compatibility.
	if i == 0 && len(p.field) > 1 && bytes.HasSuffix(p.field[1], []byte("First>")) {
		if p.parsedRange {
			return p.rangeStart, p.rangeEnd
		}
		mf := reRange.FindStringSubmatch(p.scanner.Text())
		if mf == nil || !p.scanner.Scan() {
			p.setError(errIncorrectLegacyRange)
			return x, x
		}
		// Using Bytes would be more efficient here, but Text is a lot easier
		// and this is not a frequent case.
		ml := reRange.FindStringSubmatch(p.scanner.Text())
		if ml == nil || mf[2] != ml[2] || ml[3] != "Last" || mf[4] != ml[4] {
			p.setError(errIncorrectLegacyRange)
			return x, x
		}
		p.rangeStart, p.rangeEnd = x, p.parseRune(p.scanner.Bytes()[:len(ml[1])])
		p.parsedRange = true
		return p.rangeStart, p.rangeEnd
	}
	return x, x
}

// bools recognizes all valid UCD boolean values.
var bools = map[string]bool{
	"":      false,
	"N":     false,
	"No":    false,
	"F":     false,
	"False": false,
	"Y":     true,
	"Yes":   true,
	"T":     true,
	"True":  true,
}

// Bool parses and returns field i as a boolean value.
func (p *Parser) Bool(i int) bool {
	b := p.getField(i)
	for s, v := range bools {
		if bstrEq(b, s) {
			return v
		}
	}
	p.setError(strconv.ErrSyntax)
	return false
}

// Int parses and returns field i as an integer value.
func (p *Parser) Int(i int) int {
	x, err := strconv.ParseInt(string(p.getField(i)), 10, 64)
	p.setError(err)
	return int(x)
}

// Uint parses and returns field i as an unsigned integer value.
func (p *Parser) Uint(i int) uint {
	x, err := strconv.ParseUint(string(p.getField(i)), 10, 64)
	p.setError(err)
	return uint(x)
}

// Float parses and returns field i as a decimal value.
func (p *Parser) Float(i int) float64 {
	x, err := strconv.ParseFloat(string(p.getField(i)), 64)
	p.setError(err)
	return x
}

// String parses and returns field i as a string value.
func (p *Parser) String(i int) string {
	return string(p.getField(i))
}

// Strings parses and returns field i as a space-separated list of strings.
func (p *Parser) Strings(i int) []string {
	ss := strings.Split(string(p.getField(i)), " ")
	for i, s := range ss {
		ss[i] = strings.TrimSpace(s)
	}
	return ss
}

// Comment returns the comments for the current line.
func (p *Parser) Comment() string {
	return string(p.comment)
}

var errUndefinedEnum = errors.New("ucd: undefined enum value")

// Enum interprets and returns field i as a value that must be one of the values
// in enum.
func (p *Parser) Enum(i int, enum ...string) string {
	b := p.getField(i)
	for _, s := range enum {
		if bstrEq(b, s) {
			return s
		}
	}
	p.setError(errUndefinedEnum)
	return ""
}

func bstrEq(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i, c := range b {
		if c != s[i] {
			return false
		}
	}
	return true
}
