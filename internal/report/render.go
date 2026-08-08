package report

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/go-pdf/fpdf"
)

const (
	pageWidth    = 210.0
	pageHeight   = 297.0
	marginLeft   = 16.0
	marginTop    = 16.0
	marginBottom = 18.0
	contentWidth = pageWidth - 2*marginLeft

	labelWidth = 30.0
	lineHeight = 4.6
)

type rgb struct{ r, g, b int }

var (
	colText      = rgb{28, 30, 33}
	colMuted     = rgb{112, 118, 126}
	colRule      = rgb{212, 216, 221}
	colPanel     = rgb{244, 245, 247}
	colAccent    = rgb{17, 60, 110}
	colCompliant = rgb{18, 105, 58}
	colBreach    = rgb{168, 30, 40}
	colWhite     = rgb{255, 255, 255}
)

type renderer struct {
	pdf   *fpdf.Fpdf
	title string
}

func newRenderer(title string) *renderer {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(marginLeft, marginTop, marginLeft)
	pdf.SetAutoPageBreak(true, marginBottom)
	pdf.SetCellMargin(0)
	pdf.SetTitle(title, true)
	pdf.SetCreator("halyk-agent", true)

	pdf.AddUTF8FontFromBytes("sans", "", _fontSans)
	pdf.AddUTF8FontFromBytes("sans", "B", _fontSansBold)
	pdf.AddUTF8FontFromBytes("mono", "", _fontMono)

	r := &renderer{pdf: pdf, title: title}
	pdf.AliasNbPages("{nb}")
	pdf.SetFooterFunc(r.footer)
	return r
}

func (r *renderer) footer() {
	if r.pdf.PageNo() == 1 {
		return
	}
	r.pdf.SetY(-12)
	r.font("", 7.5)
	r.textColor(colMuted)
	r.pdf.CellFormat(contentWidth/2, 5, r.title, "", 0, "L", false, 0, "")
	r.pdf.CellFormat(contentWidth/2, 5, fmt.Sprintf("%d / {nb}", r.pdf.PageNo()), "", 0, "R", false, 0, "")
	r.textColor(colText)
}

func (r *renderer) font(style string, size float64) { r.pdf.SetFont("sans", style, size) }

func (r *renderer) monoFont(size float64) { r.pdf.SetFont("mono", "", size) }

func (r *renderer) textColor(c rgb) { r.pdf.SetTextColor(c.r, c.g, c.b) }

func (r *renderer) fillColor(c rgb) { r.pdf.SetFillColor(c.r, c.g, c.b) }

func (r *renderer) drawColor(c rgb) { r.pdf.SetDrawColor(c.r, c.g, c.b) }

func (r *renderer) space(h float64) { r.pdf.SetY(r.pdf.GetY() + h) }

func (r *renderer) need(h float64) {
	if r.pdf.GetY()+h > pageHeight-marginBottom {
		r.pdf.AddPage()
	}
}

func (r *renderer) rule() {
	y := r.pdf.GetY()
	r.drawColor(colRule)
	r.pdf.SetLineWidth(0.2)
	r.pdf.Line(marginLeft, y, marginLeft+contentWidth, y)
	r.pdf.SetY(y + 1.6)
}

func (r *renderer) heading1(s string) {
	r.need(18)
	r.font("B", 17)
	r.textColor(colAccent)
	r.pdf.MultiCell(contentWidth, 8, clean(s), "", "L", false)
	r.textColor(colText)
	r.space(1)
}

func (r *renderer) heading2(s string) {
	r.need(16)
	r.font("B", 11.5)
	r.textColor(colAccent)
	r.pdf.MultiCell(contentWidth, 6, clean(s), "", "L", false)
	r.textColor(colText)
	r.space(0.5)
	r.rule()
}

func (r *renderer) heading3(s string) {
	r.need(12)
	r.font("B", 9.5)
	r.pdf.MultiCell(contentWidth, 5, clean(s), "", "L", false)
	r.space(0.5)
}

func (r *renderer) para(s string) {
	r.font("", 9)
	r.textColor(colText)
	r.pdf.SetX(marginLeft)
	r.pdf.MultiCell(contentWidth, lineHeight, clean(s), "", "L", false)
	r.space(1)
}

func (r *renderer) muted(s string, size float64) {
	r.font("", size)
	r.textColor(colMuted)
	r.pdf.SetX(marginLeft)
	r.pdf.MultiCell(contentWidth, lineHeight, clean(s), "", "L", false)
	r.textColor(colText)
}

func (r *renderer) kv(label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	value = clean(value)
	r.font("", 8.5)
	lines := r.pdf.SplitText(value, contentWidth-labelWidth)
	r.need(float64(len(lines))*lineHeight + 1)

	y := r.pdf.GetY()
	r.textColor(colMuted)
	r.pdf.SetXY(marginLeft, y)
	r.pdf.CellFormat(labelWidth, lineHeight, clean(label), "", 0, "L", false, 0, "")

	r.textColor(colText)
	r.font("", 9)
	r.pdf.SetXY(marginLeft+labelWidth, y)
	r.pdf.MultiCell(contentWidth-labelWidth, lineHeight, value, "", "L", false)
}

func (r *renderer) kvColored(label, value string, c rgb) {
	y := r.pdf.GetY()
	r.font("", 8.5)
	r.textColor(colMuted)
	r.pdf.SetXY(marginLeft, y)
	r.pdf.CellFormat(labelWidth, lineHeight, clean(label), "", 0, "L", false, 0, "")
	r.font("B", 9)
	r.textColor(c)
	r.pdf.MultiCell(contentWidth-labelWidth, lineHeight, clean(value), "", "L", false)
	r.textColor(colText)
}

func (r *renderer) badge(text string, c rgb) {
	r.font("B", 8)
	w := r.pdf.GetStringWidth(text) + 6
	x := marginLeft + contentWidth - w
	y := r.pdf.GetY()
	r.fillColor(c)
	r.pdf.Rect(x, y, w, 5.4, "F")
	r.textColor(colWhite)
	r.pdf.SetXY(x, y)
	r.pdf.CellFormat(w, 5.4, text, "", 0, "C", false, 0, "")
	r.textColor(colText)
	r.pdf.SetXY(marginLeft, y)
}

func (r *renderer) quote(s string) {
	s = clean(collapse(s))
	if s == "" {
		return
	}
	const inset = 4.0
	r.font("", 8.5)
	lines := r.pdf.SplitText(s, contentWidth-inset)
	r.textColor(colMuted)
	for _, line := range lines {
		r.need(lineHeight + 1)
		y := r.pdf.GetY()
		r.drawColor(colRule)
		r.pdf.SetLineWidth(0.6)
		r.pdf.Line(marginLeft+0.6, y, marginLeft+0.6, y+lineHeight)
		r.pdf.SetXY(marginLeft+inset, y)
		r.pdf.CellFormat(contentWidth-inset, lineHeight, line, "", 1, "L", false, 0, "")
	}
	r.textColor(colText)
	r.pdf.SetLineWidth(0.2)
}

func (r *renderer) trace(lines []string) {
	if len(lines) == 0 {
		return
	}
	const (
		inset = 2.0
		h     = 4.2
	)
	r.monoFont(7.2)
	r.fillColor(colPanel)
	r.textColor(colText)
	for _, raw := range lines {
		for _, line := range r.pdf.SplitText(clean(raw), contentWidth-2*inset) {
			r.need(h)
			r.pdf.SetX(marginLeft)
			r.pdf.CellFormat(inset, h, "", "", 0, "L", true, 0, "")
			r.pdf.CellFormat(contentWidth-2*inset, h, line, "", 0, "L", true, 0, "")
			r.pdf.CellFormat(inset, h, "", "", 1, "L", true, 0, "")
		}
	}
	r.font("", 9)
}

type column struct {
	title string
	width float64
	align string
}

func (r *renderer) tableHeader(cols []column) {
	r.need(14)
	r.font("B", 7.5)
	r.textColor(colMuted)
	r.pdf.SetX(marginLeft)
	for _, c := range cols {
		r.cellIn(c, clean(c.title))
	}
	r.pdf.Ln(5)
	r.textColor(colText)
	r.rule()
}

func (r *renderer) tableRow(cols []column, values []string, colors []*rgb, zebra bool) {
	r.need(6)
	y := r.pdf.GetY()
	if zebra {
		r.fillColor(colPanel)
		r.pdf.Rect(marginLeft, y-0.4, contentWidth, 5.4, "F")
	}
	r.pdf.SetXY(marginLeft, y)
	for i, c := range cols {
		value := ""
		if i < len(values) {
			value = clean(values[i])
		}
		colour := colText
		style := ""
		if i < len(colors) && colors[i] != nil {
			colour, style = *colors[i], "B"
		}
		r.font(style, 8)
		r.textColor(colour)
		r.cellIn(c, value)
	}
	r.textColor(colText)
	r.pdf.Ln(5)
}

func (r *renderer) cellIn(c column, value string) {
	const gutter = 2.0
	text := r.fit(value, c.width-gutter)
	if c.align == "R" {
		r.pdf.CellFormat(c.width-gutter, 5, text, "", 0, "R", false, 0, "")
		r.pdf.CellFormat(gutter, 5, "", "", 0, "L", false, 0, "")
		return
	}
	r.pdf.CellFormat(c.width, 5, text, "", 0, c.align, false, 0, "")
}

func (r *renderer) fit(s string, w float64) string {
	if r.pdf.GetStringWidth(s) <= w-1 {
		return s
	}
	runes := []rune(s)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		if r.pdf.GetStringWidth(string(runes)+"…") <= w-1 {
			return string(runes) + "…"
		}
	}
	return string(runes)
}

func (r *renderer) tiles(items [][2]string) {
	if len(items) == 0 {
		return
	}
	const (
		gap = 3.0
		h   = 18.0
	)
	r.need(h + 2)
	w := (contentWidth - gap*float64(len(items)-1)) / float64(len(items))
	y := r.pdf.GetY()
	for i, it := range items {
		x := marginLeft + float64(i)*(w+gap)
		r.fillColor(colPanel)
		r.pdf.Rect(x, y, w, h, "F")

		r.font("B", 15)
		r.textColor(colAccent)
		r.pdf.SetXY(x, y+2.5)
		r.pdf.CellFormat(w, 8, clean(it[0]), "", 0, "C", false, 0, "")

		r.font("", 7)
		r.textColor(colMuted)
		r.pdf.SetXY(x, y+10.5)
		r.pdf.CellFormat(w, 5, clean(it[1]), "", 0, "C", false, 0, "")
	}
	r.textColor(colText)
	r.pdf.SetY(y + h + 3)
}

func clean(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteRune('?')
		case r == '\r':
		case unicode.IsControl(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
