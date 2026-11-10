package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	dimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	// popupBg fills the whole box, so nothing behind it shows through.
	popupBg = lipgloss.Color("235")
	// popupHintStyle is helpStyle made bright enough to read on popupBg.
	popupHintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("248"))
)

// popupFrameW is the width popupStyle adds around its content: the border and
// two cells of padding each side.
const popupFrameW = 6

var popupStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("212")).Padding(0, 2)

// popupBox frames content, wrapping it to fit a width-wide screen.
func popupBox(content string, width int, border lipgloss.Color) string {
	if inner := max(1, width-popupFrameW); lipgloss.Width(content) > inner {
		content = lipgloss.NewStyle().Width(inner).Render(content)
	}
	return popupStyle.BorderForeground(border).Render(content)
}

// opaque gives every cell of box popupBg. Styled text inside the box ends
// with a reset, which would drop the background for the rest of its line, so
// the background is turned back on after each reset.
func opaque(box string) string {
	on, _, found := strings.Cut(lipgloss.NewStyle().Background(popupBg).Render("x"), "x")
	if !found || on == "" { // a terminal without colors
		return box
	}
	lines := strings.Split(box, "\n")
	for i, l := range lines {
		l = strings.ReplaceAll(l, "\x1b[0m", "\x1b[0m"+on)
		l = strings.ReplaceAll(l, "\x1b[m", "\x1b[m"+on)
		lines[i] = on + l + "\x1b[0m"
	}
	return strings.Join(lines, "\n")
}

// gutter surrounds box with a blank cell left and right and a blank row above
// and below, where the screen has room, so the text behind doesn't run up to
// the border.
func gutter(box string, width, height int) string {
	lines := strings.Split(box, "\n")
	w := lipgloss.Width(box)
	if width >= w+2 {
		for i, l := range lines {
			lines[i] = " " + l + " "
		}
		w += 2
	}
	if height >= len(lines)+2 {
		blank := strings.Repeat(" ", w)
		lines = append(append([]string{blank}, lines...), blank)
	}
	return strings.Join(lines, "\n")
}

// popup draws box centered on a width×height screen, over bg dimmed so the
// box stands out from what's behind it.
func popup(bg, box string, width, height int) string {
	if width <= 0 || height <= 0 { // no size reported yet
		return box
	}
	box = gutter(opaque(box), width, height)
	bgLines := strings.Split(bg, "\n")
	rows := make([]string, height)
	for i := range rows {
		var l string
		if i < len(bgLines) {
			l = ansi.Strip(bgLines[i])
		}
		rows[i] = fit(l, width)
	}
	boxLines := strings.Split(box, "\n")
	boxW := min(width, lipgloss.Width(box))
	x, y := (width-boxW)/2, max(0, (height-len(boxLines))/2)
	for i := range rows {
		j := i - y
		if j < 0 || j >= len(boxLines) {
			rows[i] = dimStyle.Render(rows[i])
			continue
		}
		left := ansi.Truncate(rows[i], x, "")
		right := ansi.TruncateLeft(rows[i], x+boxW, "")
		// A wide character cut at an edge leaves the row short; pad it back.
		mid := fit(boxLines[j], width-ansi.StringWidth(left)-ansi.StringWidth(right))
		rows[i] = dimStyle.Render(left) + mid + dimStyle.Render(right)
	}
	return strings.Join(rows, "\n")
}

// fit truncates or pads s to exactly w cells.
func fit(s string, w int) string {
	s = ansi.Truncate(s, max(0, w), "")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}
