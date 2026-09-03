package diff

import (
	"fmt"
	"strings"
)

// ponytail: standard dynamic programming LCS for line-by-line diff. Ceiling: memory O(N*M) for very large files, suitable for config/compose files under 5000 lines. Upgrade path: Myers diff algorithm.
func UnifiedDiff(nameA, textA, nameB, textB string) string {
	linesA := strings.Split(textA, "\n")
	linesB := strings.Split(textB, "\n")

	m := len(linesA)
	n := len(linesB)

	// Build LCS matrix
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if linesA[i-1] == linesB[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Backtrack to find diff
	var diffLines []string
	i, j := m, n
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && linesA[i-1] == linesB[j-1] {
			diffLines = append(diffLines, "  "+linesA[i-1])
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			diffLines = append(diffLines, "+ "+linesB[j-1])
			j--
		} else if i > 0 && (j == 0 || dp[i][j-1] < dp[i-1][j]) {
			diffLines = append(diffLines, "- "+linesA[i-1])
			i--
		}
	}

	// Reverse to get normal order
	for k := 0; k < len(diffLines)/2; k++ {
		opp := len(diffLines) - 1 - k
		diffLines[k], diffLines[opp] = diffLines[opp], diffLines[k]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- %s (Desired State)\n", nameA))
	sb.WriteString(fmt.Sprintf("+++ %s (Actual State on Node)\n", nameB))
	for _, l := range diffLines {
		sb.WriteString(l)
		sb.WriteString("\n")
	}

	return strings.TrimSpace(sb.String())
}
