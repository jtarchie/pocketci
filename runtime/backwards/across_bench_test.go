package backwards_test

import (
	"fmt"
	"testing"

	config "github.com/jtarchie/pocketci/backwards"
	backwards "github.com/jtarchie/pocketci/runtime/backwards"
)

func makeAcrossVars(numVars, valuesPerVar int) []config.AcrossVar {
	vars := make([]config.AcrossVar, numVars)

	for i := range vars {
		vals := make([]string, valuesPerVar)

		for j := range vals {
			vals[j] = fmt.Sprintf("v%d-%d", i, j)
		}

		vars[i] = config.AcrossVar{
			Var:    fmt.Sprintf("var%d", i),
			Values: vals,
		}
	}

	return vars
}

func BenchmarkGenerateCombinations_2x10(b *testing.B) {
	benchmarkGenerateCombinations(b, 2, 10)
}

func BenchmarkGenerateCombinations_3x10(b *testing.B) {
	benchmarkGenerateCombinations(b, 3, 10)
}

func BenchmarkGenerateCombinations_4x5(b *testing.B) {
	benchmarkGenerateCombinations(b, 4, 5)
}

func benchmarkGenerateCombinations(b *testing.B, numVars, valuesPerVar int) {
	b.Helper()

	vars := makeAcrossVars(numVars, valuesPerVar)

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		_ = backwards.ExportGenerateCombinations(vars)
	}
}
