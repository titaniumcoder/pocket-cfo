package api

import (
	"fmt"
	"strings"
)

const diffContext = 3

func LineDiff(before, after string) (string, int) {
	a := splitLines(before)
	b := splitLines(after)
	ops := diffOps(a, b)
	var out strings.Builder
	changed := 0
	i := 0
	for i < len(ops) {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(0, i-diffContext)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run-end > 2*diffContext || run == len(ops) {
				end = min(len(ops), end+diffContext)
				break
			}
			end = run
		}
		fmt.Fprintf(&out, "@@ -%d +%d @@\n", ops[start].aLine, ops[start].bLine)
		for _, op := range ops[start:end] {
			out.WriteString(string(op.kind) + op.text + "\n")
			if op.kind != ' ' {
				changed++
			}
		}
		i = end
	}
	return out.String(), changed
}

type diffOp struct {
	kind  byte
	text  string
	aLine int
	bLine int
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i], i + 1, j + 1})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, diffOp{'-', a[i], i + 1, j + 1})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j], i + 1, j + 1})
			j++
		}
	}
	return ops
}
