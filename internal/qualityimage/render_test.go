package qualityimage

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestTerminalImageEscapesMarkupAndKeepsColorsAndCJK(t *testing.T) {
	image, err := Render([]byte(`{"Head":{"IP":"203.0.113.8"}}`), "出口 203.0.113.8\n\x1b[1;31m风险\x1b[0m <script>alert('x')</script> & \"quote\"\n\x1b[42m 可访问 \x1b[0m\x1b]8;;https://attacker.example\a链接\x1b]8;;\a")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(image, []byte("<script>")) || bytes.Contains(image, []byte("attacker.example")) || bytes.ContainsRune(image, '\x1b') {
		t.Fatal("untrusted content became markup or link")
	}
	for _, want := range []string{"#ee6b75", "#63c99d", "font-weight=\"700\"", "风险", "&lt;script&gt;", "&amp;", "&#34;quote&#34;", "textLength=\"40\""} {
		if !bytes.Contains(image, []byte(want)) {
			t.Fatalf("missing %q", want)
		}
	}
	d := xml.NewDecoder(bytes.NewReader(image))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := tok.(xml.StartElement); ok {
			switch e.Name.Local {
			case "svg", "title", "rect", "text", "path", "g":
			default:
				t.Fatalf("unexpected element %s", e.Name.Local)
			}
			for _, a := range e.Attr {
				if strings.HasPrefix(a.Name.Local, "on") || a.Name.Local == "href" {
					t.Fatalf("active attribute %s", a.Name.Local)
				}
			}
		}
	}
}

func TestLegacyImageIncludesEverySavedSectionAndLabelsOrigin(t *testing.T) {
	image, err := Render([]byte(`{"Head":{"IP":"203.0.113.8"},"Info":{"Organization":"网络组织"},"Type":{},"Score":{"Provider":0},"Factor":{"VPN":{"Provider":false}},"Media":{"ChatGPT":{"Status":"Yes"}},"Mail":{"Status":"smtp-not-tested"}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"根据已保存的 JSON 报告生成", "基础信息", "用途与公司类型", "信誉评分", "风险标记", "可访问性", "邮件与", "DNS", "黑名单", "未检测", "网络组织", "否", "ChatGPT", "未知"} {
		if !bytes.Contains(image, []byte(want)) {
			t.Fatalf("missing factual field %s", want)
		}
	}
}

func TestImageRejectsOversizedOrInvalidText(t *testing.T) {
	for _, ansi := range []string{strings.Repeat("a", MaxANSI+1), string([]byte{0xff}), "text" + strings.Repeat("\n", 2049)} {
		if _, err := Render([]byte(`{}`), ansi); err == nil {
			t.Fatal("unbounded or invalid text accepted")
		}
	}
	if _, err := Render([]byte(`not-json`), "hello"); err == nil {
		t.Fatal("malformed report accepted")
	}
}

func TestTablePaddingPositionsWordsWithoutStretchingTheirGlyphs(t *testing.T) {
	image, err := Render([]byte(`{}`), "IPQS                  未知\n\x1b[42m 家宽     \x1b[0m")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`x="28" y="80" fill="#e5e9f0" textLength="40"`, `x="248" y="80" fill="#e5e9f0" textLength="40"`, `width="100" height="24" fill="#63c99d"`} {
		if !bytes.Contains(image, []byte(want)) {
			t.Fatalf("incorrect terminal column positioning: %s\n%s", want, image)
		}
	}
	if bytes.Contains(image, []byte("IPQS  ")) {
		t.Fatal("table padding must not be combined with glyph textLength")
	}
}
