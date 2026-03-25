package jsapi

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"slices"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/dop251/goja"
	"github.com/pmezard/go-difflib/difflib"
)

// MaxDepth defines the maximum depth for spew dumping.
const (
	MaxDepth      = 10
	SpacingMargin = 100
)

type Assert struct {
	logger *slog.Logger
	vm     *goja.Runtime
}

func NewAssert(vm *goja.Runtime, logger *slog.Logger) *Assert {
	logger.Debug("handler.created")

	return &Assert{
		logger: logger.WithGroup("assert.created"),
		vm:     vm,
	}
}

var ErrAssertion = errors.New("assertion failed")

func (a *Assert) Equal(actual, expected any, message ...string) {
	a.logger.Debug("equality.checking",
		"expected_type", fmt.Sprintf("%T", expected),
		"actual_type", fmt.Sprintf("%T", actual))

	if !reflect.DeepEqual(actual, expected) {
		diff := diff(expected, actual)
		expectedFmt, actualFmt := formatUnequalValues(expected, actual)
		msg := fmt.Sprintf("Not equal: \n"+
			"expected: %s\n"+
			"actual  : %s%s", expectedFmt, actualFmt, diff)

		if len(message) > 0 {
			msg = message[0] + "\n" + msg
		}

		a.fail(msg)
	}
}

func (a *Assert) NotEqual(actual, expected any, message ...string) {
	a.logger.Debug("inequality.checking",
		"expected_type", fmt.Sprintf("%T", expected),
		"actual_type", fmt.Sprintf("%T", actual))

	if expected == actual {
		msg := fmt.Sprintf("expected not %v, but got %v", expected, actual)
		if len(message) > 0 {
			msg = message[0]
		}

		a.fail(msg)
	}
}

func (a *Assert) ContainsString(str, substr string, message ...string) {
	// Redact potentially sensitive string data in logs
	a.logger.Debug("substring.checking",
		"pattern_length", len(substr),
		"string_length", len(str))

	matcher, err := regexp.Compile(substr)
	if err != nil {
		a.logger.Debug("regex.failed", "err", err)
		a.fail(fmt.Sprintf("invalid regular expression: %s", err))

		return
	}

	if !matcher.MatchString(str) {
		msg := fmt.Sprintf("expected %q to contain %q", str, substr)
		if len(message) > 0 {
			msg = message[0]
		}

		a.fail(msg)
	}
}

// EventuallyContainsStringOptions configures the polling behavior.
type EventuallyContainsStringOptions struct {
	TimeoutMs  int64  `json:"timeoutMs"`
	IntervalMs int64  `json:"intervalMs"`
	Message    string `json:"message"`
}

// parseEventuallyOptions parses the third+ arguments as either an options
// object or deprecated positional args.
func (a *Assert) parseEventuallyOptions(args []goja.Value) (timeoutMs, intervalMs int64, message string) {
	if len(args) == 0 {
		return 0, 0, ""
	}

	arg := args[0]
	if goja.IsUndefined(arg) || goja.IsNull(arg) {
		return 0, 0, ""
	}

	obj := arg.ToObject(a.vm)
	if obj == nil {
		return 0, 0, ""
	}

	// Options object form: { timeoutMs, intervalMs, message }
	if obj.Get("timeoutMs") != nil || obj.Get("intervalMs") != nil || obj.Get("message") != nil {
		var opts EventuallyContainsStringOptions
		if err := a.vm.ExportTo(arg, &opts); err == nil {
			return opts.TimeoutMs, opts.IntervalMs, opts.Message
		}

		return 0, 0, ""
	}

	// Deprecated positional form: timeoutMs, intervalMs, message
	timeoutMs = arg.ToInteger()
	if len(args) >= 2 {
		intervalMs = args[1].ToInteger()
	}

	if len(args) >= 3 {
		message = args[2].String()
	}

	return timeoutMs, intervalMs, message
}

// EventuallyContainsString polls a getter function until the returned string
// matches substr or the timeout expires.
//
// Accepts two calling conventions:
//
//	eventuallyContainsString(getter, substr, { timeoutMs, intervalMs, message })
//	eventuallyContainsString(getter, substr, timeoutMs, intervalMs, message)  // deprecated
func (a *Assert) EventuallyContainsString(call goja.FunctionCall) goja.Value {
	if len(call.Arguments) < 2 {
		a.fail("eventuallyContainsString requires at least getter and substr arguments")
		return goja.Undefined()
	}

	getter := call.Arguments[0]
	substr := call.Arguments[1].String()
	timeoutMs, intervalMs, message := a.parseEventuallyOptions(call.Arguments[2:])

	a.logger.Debug("substring.eventually.checking",
		"pattern_length", len(substr),
		"timeout_ms", timeoutMs,
		"interval_ms", intervalMs)

	matcher, err := regexp.Compile(substr)
	if err != nil {
		a.logger.Debug("regex.failed", "err", err)
		a.fail(fmt.Sprintf("invalid regular expression: %s", err))

		return goja.Undefined()
	}

	getterFunc, ok := goja.AssertFunction(getter)
	if !ok {
		a.fail("expected getter to be a function")

		return goja.Undefined()
	}

	if timeoutMs <= 0 {
		timeoutMs = 1000
	}

	if intervalMs <= 0 {
		intervalMs = 50
	}

	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	interval := time.Duration(intervalMs) * time.Millisecond
	var last string

	for {
		value, callErr := getterFunc(goja.Undefined())
		if callErr != nil {
			a.fail(fmt.Sprintf("eventually getter failed: %v", callErr))

			return goja.Undefined()
		}

		last = fmt.Sprintf("%v", value.Export())
		if matcher.MatchString(last) {
			return goja.Undefined()
		}

		if !time.Now().Before(deadline) {
			break
		}

		time.Sleep(interval)
	}

	if message == "" {
		message = fmt.Sprintf(
			"expected %q to eventually contain %q within %s",
			last,
			substr,
			time.Duration(timeoutMs)*time.Millisecond,
		)
	}

	a.fail(message)

	return goja.Undefined()
}

func (a *Assert) Truthy(value bool, message ...string) {
	a.logger.Debug("truthiness.checking", "value_type", fmt.Sprintf("%T", value))

	if !value {
		msg := fmt.Sprintf("expected %v to be truthy", value)
		if len(message) > 0 {
			msg = message[0]
		}

		a.fail(msg)
	}
}

func (a *Assert) ContainsElement(array []any, element any, message ...string) {
	a.logger.Debug("element.checking",
		"element_type", fmt.Sprintf("%T", element),
		"array_length", len(array))

	found := slices.Contains(array, element)

	if !found {
		msg := fmt.Sprintf("expected array to contain %v", element)
		if len(message) > 0 {
			msg = message[0]
		}

		a.fail(msg)
	}
}

func (a *Assert) fail(message string) {
	a.logger.Error("assertion.fail.failed", "err", message)
	a.vm.Interrupt(fmt.Errorf("%w: %s", ErrAssertion, message))
}

var spewConfig = spew.ConfigState{
	Indent:                  " ",
	DisablePointerAddresses: true,
	DisableCapacities:       true,
	SortKeys:                true,
	DisableMethods:          true,
	MaxDepth:                MaxDepth,
}

var spewConfigStringerEnabled = spew.ConfigState{
	Indent:                  " ",
	DisablePointerAddresses: true,
	DisableCapacities:       true,
	SortKeys:                true,
	MaxDepth:                MaxDepth,
}

func typeAndKind(v any) (reflect.Type, reflect.Kind) {
	valType := reflect.TypeOf(v)
	valKind := valType.Kind()

	if valKind == reflect.Pointer {
		valType = valType.Elem()
		valKind = valType.Kind()
	}

	return valType, valKind
}

func diff(expected any, actual any) string {
	if expected == nil || actual == nil {
		return ""
	}

	expectedType, expectedKind := typeAndKind(expected)
	actualType, _ := typeAndKind(actual)

	if expectedType != actualType {
		return ""
	}

	if expectedKind != reflect.Struct && expectedKind != reflect.Map && expectedKind != reflect.Slice && expectedKind != reflect.Array && expectedKind != reflect.String {
		return ""
	}

	var expectedStr, actualStr string

	switch expectedType {
	case reflect.TypeFor[string]():
		expectedStr = reflect.ValueOf(expected).String()
		actualStr = reflect.ValueOf(actual).String()
	case reflect.TypeFor[time.Time]():
		expectedStr = spewConfigStringerEnabled.Sdump(expected)
		actualStr = spewConfigStringerEnabled.Sdump(actual)
	default:
		expectedStr = spewConfig.Sdump(expected)
		actualStr = spewConfig.Sdump(actual)
	}

	diff, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(expectedStr),
		B:        difflib.SplitLines(actualStr),
		FromFile: "Expected",
		FromDate: "",
		ToFile:   "Actual",
		ToDate:   "",
		Context:  1,
	})

	return "\n\nDiff:\n" + diff
}

func formatUnequalValues(expected, actual any) (string, string) {
	if reflect.TypeOf(expected) != reflect.TypeOf(actual) {
		return fmt.Sprintf("%T(%s)", expected, truncatingFormat(expected)),
			fmt.Sprintf("%T(%s)", actual, truncatingFormat(actual))
	}

	if _, ok := expected.(time.Duration); ok {
		return fmt.Sprintf("%v", expected), fmt.Sprintf("%v", actual)
	}

	return truncatingFormat(expected), truncatingFormat(actual)
}

func truncatingFormat(data any) string {
	value := fmt.Sprintf("%#v", data)
	maxCap := bufio.MaxScanTokenSize - SpacingMargin // Give us some space the type info too if needed.

	if len(value) > maxCap {
		value = value[0:maxCap] + "<... truncated>"
	}

	return value
}
