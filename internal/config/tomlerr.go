package config

import (
	"strings"

	"github.com/BurntSushi/toml"
)

// ParseErrorLine returns the 1-based line of a TOML syntax error, or 0 if err
// is not one. The parser reports the line after the newline when it runs into
// an unexpected end of line, which is the wrong line for a user to look at.
func ParseErrorLine(err error) int {
	pe, ok := err.(toml.ParseError)
	if !ok {
		return 0
	}
	line := pe.Position.Line
	if strings.Contains(pe.Message, `got '\n'`) && line > 1 {
		line--
	}
	return line
}
