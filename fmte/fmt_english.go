package fmte

import (
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"os"
)

var p *message.Printer

func init() {
	p = message.NewPrinter(language.English)
}

// Printf is an alternative to fmt.Printf with formatting improvements
func Printf(format string, a ...interface{}) {
	_, _ = p.Printf(format, a...)
}

// PrintfErr is an alternative to fmt.Printf to StdErr with formatting improvements
func PrintfErr(format string, a ...interface{}) {
	_, _ = p.Fprintf(os.Stderr, format, a...)
}

// Sprintf is an alternative to fmt.Sprintf with formatting improvements
func Sprintf(format string, a ...interface{}) string {
	return p.Sprintf(format, a...)
}
