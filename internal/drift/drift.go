package drift

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Snapshot represents the configuration state (Compose YAML and environment variables)
// of either a Desired State or an Actual State on a Node.
type Snapshot struct {
	ComposeYAML string
	EnvVars     string
}

// DriftResult contains the outcome of a Drift Check between Desired State and Actual State.
type DriftResult struct {
	IsDrifted   bool
	DesiredHash string
	ActualHash  string
	Diff        string
}

// ComputeHash computes the SHA-256 digest of stack configuration (ComposeYAML and EnvVars separated by "\n---\n"),
// formatted as lowercase hexadecimal.
func ComputeHash(composeYAML, envVars string) string {
	sum := sha256.Sum256([]byte(composeYAML + "\n---\n" + envVars))
	return hex.EncodeToString(sum[:])
}

// Evaluate performs a Drift Check comparing the Desired State snapshot against the Actual State snapshot.
// If the hashes match, IsDrifted is false and Diff is empty.
// If the hashes differ, IsDrifted is true and a unified diff is generated for the modified components.
func Evaluate(desired, actual Snapshot) DriftResult {
	desiredHash := ComputeHash(desired.ComposeYAML, desired.EnvVars)
	actualHash := ComputeHash(actual.ComposeYAML, actual.EnvVars)

	if desiredHash == actualHash {
		return DriftResult{
			IsDrifted:   false,
			DesiredHash: desiredHash,
			ActualHash:  actualHash,
		}
	}

	var diffParts []string
	if desired.ComposeYAML != actual.ComposeYAML {
		diffParts = append(diffParts, UnifiedDiff("docker-compose.yml (Desired State)", desired.ComposeYAML, "docker-compose.yml (Actual State)", actual.ComposeYAML))
	}
	if desired.EnvVars != actual.EnvVars {
		diffParts = append(diffParts, UnifiedDiff(".env (Desired State)", desired.EnvVars, ".env (Actual State)", actual.EnvVars))
	}

	return DriftResult{
		IsDrifted:   true,
		DesiredHash: desiredHash,
		ActualHash:  actualHash,
		Diff:        strings.Join(diffParts, "\n\n"),
	}
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// UnifiedDiff computes a line-by-line diff between textA and textB with unified diff headers.
//
// ponytail: standard dynamic programming LCS for line-by-line diff. Ceiling: memory O(N*M) for very large files, suitable for config/compose files under 5000 lines. Upgrade path: Myers diff algorithm.
func UnifiedDiff(nameA, textA, nameB, textB string) string {
	if textA == textB {
		return ""
	}

	linesA := splitLines(textA)
	linesB := splitLines(textB)

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
	sb.WriteString(fmt.Sprintf("--- %s\n", nameA))
	sb.WriteString(fmt.Sprintf("+++ %s\n", nameB))
	for _, l := range diffLines {
		sb.WriteString(l)
		sb.WriteString("\n")
	}

	return strings.TrimSpace(sb.String())
}
