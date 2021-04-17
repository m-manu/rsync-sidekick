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

func Printf(format string, a ...interface{}) {
	_, _ = p.Printf(format, a...)
}

func PrintfErr(format string, a ...interface{}) {
	_, _ = p.Fprintf(os.Stderr, format, a...)
}

func Sprintf(format string, a ...interface{}) string {
	return p.Sprintf(format, a...)
}
