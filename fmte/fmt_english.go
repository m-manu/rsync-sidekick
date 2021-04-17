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

// Printf is fmt.Printf for English
func Printf(format string, a ...interface{}) {
	_, _ = p.Printf(format, a...)
}

// PrintfErr is fmt.Printf to StdErr for English
func PrintfErr(format string, a ...interface{}) {
	_, _ = p.Fprintf(os.Stderr, format, a...)
}

// Sprintf is fmt.Sprintf for English
func Sprintf(format string, a ...interface{}) string {
	return p.Sprintf(format, a...)
}
