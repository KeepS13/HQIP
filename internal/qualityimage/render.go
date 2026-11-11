// Package qualityimage renders saved diagnostic data without a browser, shell,
// remote image service, or any markup supplied by the diagnostic.
package qualityimage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/width"
)

const MaxANSI = 64 << 10
const MaxSVG = 2 << 20

type style struct {
	fg, bg                       string
	bold, italic, underline, dim bool
}
type span struct {
	text          string
	style         style
	column, cells int
}

var palette = [...]string{"#262b35", "#ee6b75", "#63c99d", "#e6c16d", "#79a9eb", "#bd98ef", "#71cfdf", "#e5e9f0", "#788599", "#ff8992", "#87e5b5", "#f5db95", "#a0c4ff", "#d5b7ff", "#9ce8ee", "#ffffff"}

// Render converts only terminal text and SGR colors to a self-contained image.
// Existing JSON-only reports get a clearly labelled readable summary.
func Render(report json.RawMessage, ansi string) ([]byte, error) {
	if len(report) > 512<<10 || !json.Valid(report) {
		return nil, errors.New("invalid report")
	}
	if len(ansi) > MaxANSI || !utf8.ValidString(ansi) {
		return nil, errors.New("invalid diagnostic image text")
	}
	label := "IP 质量体检 · 检测原始输出"
	if strings.TrimSpace(ansi) == "" {
		label = "IP 质量体检 · 根据已保存的 JSON 报告生成"
		var err error
		ansi, err = legacySummary(report)
		if err != nil {
			return nil, err
		}
	}
	lines, columns, err := terminalLines(ansi)
	if err != nil {
		return nil, err
	}
	if columns < 72 {
		columns = 72
	}
	w, h := columns*10+56, len(lines)*24+112
	var out bytes.Buffer
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img"><title>%s</title><rect width="100%%" height="100%%" rx="14" fill="#111722"/><text x="28" y="34" fill="#a5b2ca" font-size="14" font-family="sans-serif">%s</text><path d="M28 52H%d" stroke="#2d384c"/><g font-family="Consolas,Menlo,DejaVu Sans Mono,Noto Sans Mono CJK SC,monospace" font-size="16" xml:space="preserve">`, w, h, w, h, html.EscapeString(label), html.EscapeString(label), w-28)
	for i, line := range lines {
		y := 80 + i*24
		for _, part := range line {
			x := 28 + part.column*10
			if part.style.bg != "" {
				fmt.Fprintf(&out, `<rect x="%d" y="%d" width="%d" height="24" fill="%s"/>`, x, y-18, part.cells*10, part.style.bg)
			}
			// Image renderers can collapse XML spaces before applying textLength.
			// Place words at explicit terminal columns; padding gets no glyphs and
			// never stretches the neighbouring table labels or numeric values.
			for _, part := range textRuns(part) {
				x := 28 + part.column*10
				fmt.Fprintf(&out, `<text x="%d" y="%d" fill="%s"`, x, y, part.style.fg)
				if part.style.bold {
					out.WriteString(` font-weight="700"`)
				}
				if part.style.italic {
					out.WriteString(` font-style="italic"`)
				}
				if part.style.underline {
					out.WriteString(` text-decoration="underline"`)
				}
				if part.style.dim {
					out.WriteString(` opacity="0.7"`)
				}
				// Fixed lengths preserve the upstream terminal table's CJK alignment.
				if part.cells > 0 {
					fmt.Fprintf(&out, ` textLength="%d" lengthAdjust="spacingAndGlyphs"`, part.cells*10)
				}
				out.WriteByte('>')
				out.WriteString(html.EscapeString(part.text))
				out.WriteString(`</text>`)
			}
		}
		if out.Len() > MaxSVG {
			return nil, errors.New("report image exceeds size limit")
		}
	}
	out.WriteString(`</g></svg>`)
	return out.Bytes(), nil
}

func textRuns(part span) []span {
	var runs []span
	var word strings.Builder
	column, start, cells := part.column, part.column, 0
	flush := func() {
		if word.Len() > 0 {
			runs = append(runs, span{text: word.String(), style: part.style, column: start, cells: cells})
			word.Reset()
			cells = 0
		}
	}
	for _, r := range part.text {
		w := 1
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			w = 0
		} else if kind := width.LookupRune(r).Kind(); kind == width.EastAsianWide || kind == width.EastAsianFullwidth {
			w = 2
		}
		if unicode.IsSpace(r) {
			flush()
			column += w
			start = column
			continue
		}
		if word.Len() == 0 {
			start = column
		}
		word.WriteRune(r)
		cells += w
		column += w
	}
	flush()
	return runs
}

func terminalLines(text string) ([][]span, int, error) {
	lines := [][]span{{}}
	s := style{fg: palette[7]}
	column, maximum := 0, 0
	newline := func() error {
		if column > maximum {
			maximum = column
		}
		column = 0
		if len(lines) >= 2048 {
			return errors.New("report image exceeds line limit")
		}
		lines = append(lines, []span{})
		return nil
	}
	for pos := 0; pos < len(text); {
		if text[pos] == 27 {
			pos++
			if pos < len(text) && text[pos] == '[' {
				pos++
				start := pos
				for pos < len(text) && !(text[pos] >= 0x40 && text[pos] <= 0x7e) {
					pos++
				}
				if pos < len(text) {
					if text[pos] == 'm' && pos-start <= 128 {
						applySGR(&s, text[start:pos])
					}
					pos++
				}
			} else if pos < len(text) && text[pos] == ']' {
				pos++
				for pos < len(text) {
					if text[pos] == 7 {
						pos++
						break
					}
					if text[pos] == 27 && pos+1 < len(text) && text[pos+1] == '\\' {
						pos += 2
						break
					}
					pos++
				}
			} else if pos < len(text) {
				pos++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(text[pos:])
		pos += size
		if r == '\n' {
			if err := newline(); err != nil {
				return nil, 0, err
			}
			continue
		}
		if r == '\r' {
			continue
		}
		if r == '\t' {
			r = ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0xfffe || r == 0xffff {
			continue
		}
		cells := 1
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			cells = 0
		} else if k := width.LookupRune(r).Kind(); k == width.EastAsianWide || k == width.EastAsianFullwidth {
			cells = 2
		}
		if column+cells > 160 {
			if err := newline(); err != nil {
				return nil, 0, err
			}
		}
		line := &lines[len(lines)-1]
		if len(*line) > 0 && (*line)[len(*line)-1].style == s {
			p := &(*line)[len(*line)-1]
			p.text += string(r)
			p.cells += cells
		} else {
			*line = append(*line, span{text: string(r), style: s, column: column, cells: cells})
		}
		column += cells
	}
	if column > maximum {
		maximum = column
	}
	return lines, maximum, nil
}

func applySGR(s *style, params string) {
	fields := strings.Split(params, ";")
	values := make([]int, len(fields))
	for i, p := range fields {
		if p == "" {
			continue
		}
		v, e := strconv.Atoi(p)
		if e != nil || v < 0 || v > 255 {
			return
		}
		values[i] = v
	}
	for i := 0; i < len(values); i++ {
		v := values[i]
		switch {
		case v == 0:
			*s = style{fg: palette[7]}
		case v == 1:
			s.bold = true
		case v == 2:
			s.dim = true
		case v == 3:
			s.italic = true
		case v == 4:
			s.underline = true
		case v == 22:
			s.bold = false
			s.dim = false
		case v == 23:
			s.italic = false
		case v == 24:
			s.underline = false
		case v >= 30 && v <= 37:
			s.fg = palette[v-30]
		case v >= 40 && v <= 47:
			s.bg = palette[v-40]
		case v >= 90 && v <= 97:
			s.fg = palette[v-90+8]
		case v >= 100 && v <= 107:
			s.bg = palette[v-100+8]
		case v == 39:
			s.fg = palette[7]
		case v == 49:
			s.bg = ""
		case (v == 38 || v == 48) && i+2 < len(values) && values[i+1] == 5:
			color := indexedColor(values[i+2])
			if v == 38 {
				s.fg = color
			} else {
				s.bg = color
			}
			i += 2
		case (v == 38 || v == 48) && i+4 < len(values) && values[i+1] == 2:
			color := fmt.Sprintf("#%02x%02x%02x", values[i+2], values[i+3], values[i+4])
			if v == 38 {
				s.fg = color
			} else {
				s.bg = color
			}
			i += 4
		}
	}
}

func indexedColor(n int) string {
	if n < 16 {
		return palette[n]
	}
	if n >= 232 {
		v := 8 + (n-232)*10
		return fmt.Sprintf("#%02x%02x%02x", v, v, v)
	}
	n -= 16
	levels := [...]int{0, 95, 135, 175, 215, 255}
	return fmt.Sprintf("#%02x%02x%02x", levels[n/36], levels[n/6%6], levels[n%6])
}
