package assertions

import (
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
)

// AssertEquals asserts whether actual value is equal to expected value
func AssertEquals(t *testing.T, expected, actual interface{}) {
	if expected != actual {
		t.Errorf("expected=%+v (type %v)  actual=%+v (type %v)",
			expected, reflect.TypeOf(expected), actual, reflect.TypeOf(actual))
		printCallerContext()
	}
}

// AssertTrue asserts whether actual value is true
func AssertTrue(t *testing.T, actual bool) {
	if !actual {
		fmt.Println("result was expected to be true")
		t.Fail()
		printCallerContext()
	}
}

func printCallerContext() {
	stackTraceText := string(debug.Stack())
	stackTraceLines := strings.Split(stackTraceText, "\n")
	callerContext := strings.Join(stackTraceLines[7:9], "\n")
	fmt.Println(callerContext)
}
