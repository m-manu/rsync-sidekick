package fmte

import (
	"errors"
	"os"
	"strings"
	"sync"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var p *message.Printer

var mx sync.Mutex // Shared mutex across stdout and stderr to ensure ordering across

var normalPrint = true

var verbosePrint = false

func init() {
	p = message.NewPrinter(language.English)
}

// Off function turns off print functions within fmte package
func Off() {
	normalPrint = false
}

// VerboseOn turns on verbose print functions within fmte package
func VerboseOn() {
	verbosePrint = true
}

// Printf is goroutine-safe fmt.Printf for English
func Printf(format string, a ...any) {
	if !normalPrint {
		return
	}
	mx.Lock()
	_, _ = p.Printf(format, a...)
	mx.Unlock()
}

// PrintfV is goroutine-safe fmt.Printf for English (Verbose mode)
func PrintfV(format string, a ...any) {
	if normalPrint && verbosePrint {
		mx.Lock()
		_, _ = p.Printf(format, a...)
		mx.Unlock()
	}
}

// Print is a goroutine-safe fmt.Print for English
func Print(a ...any) {
	if !normalPrint {
		return
	}
	mx.Lock()
	_, _ = p.Print(a...)
	mx.Unlock()
}

// PrintfErr is goroutine-safe fmt.Printf to StdErr for English
func PrintfErr(format string, a ...any) {
	mx.Lock()
	_, _ = p.Fprintf(os.Stderr, format, a...)
	mx.Unlock()
}

// Errors combines multiple errors into one
func Errors(message string, errs []error) error {
	var sb strings.Builder
	sb.WriteString(message)
	sb.WriteString(": ")
	for _, err := range errs {
		sb.WriteString(err.Error())
		sb.WriteString(", ")
	}
	combinedError := errors.New(sb.String())
	return combinedError
}
