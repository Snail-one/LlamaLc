package launcher

import (
	"fmt"
	"strings"
	"unicode"
)

func safeTerminalText(value string) string {
	var output strings.Builder
	for _, r := range value {
		switch r {
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		case '\t':
			output.WriteString(`\t`)
		default:
			if unicode.IsControl(r) {
				fmt.Fprintf(&output, `\u%04X`, r)
			} else {
				output.WriteRune(r)
			}
		}
	}
	return output.String()
}
