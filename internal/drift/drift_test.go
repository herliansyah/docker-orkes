package drift_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"docker-orkes/internal/drift"
)

func TestComputeHash(t *testing.T) {
	tests := []struct {
		name        string
		composeYAML string
		envVars     string
		wantHash    string
	}{
		{
			name:        "empty configuration",
			composeYAML: "",
			envVars:     "",
			wantHash:    "316deeb28892b1cdebfe5c12c2cd620b5b8f29289c1ffe3d4f5fc1b2e6a4ea7d",
		},
		{
			name:        "standard compose and env",
			composeYAML: "services:\n  app:\n    image: nginx:latest\n",
			envVars:     "PORT=80\n",
			wantHash:    "4a881f4cbb7ae707cc4d00c5f06be4e553783ea6fbcbd023f63b0becb8074cd4",
		},
		{
			name:        "redis compose",
			composeYAML: "services:\n  redis:\n    image: redis:7-alpine\n",
			envVars:     "MAX_MEMORY=256mb",
			wantHash: func() string {
				sum := sha256.Sum256([]byte("services:\n  redis:\n    image: redis:7-alpine\n\n---\nMAX_MEMORY=256mb"))
				return hex.EncodeToString(sum[:])
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := drift.ComputeHash(tt.composeYAML, tt.envVars)
			if got != tt.wantHash {
				t.Errorf("ComputeHash() = %q, want %q", got, tt.wantHash)
			}

			// Verify determinism: calling repeatedly produces identical hash
			gotAgain := drift.ComputeHash(tt.composeYAML, tt.envVars)
			if got != gotAgain {
				t.Errorf("ComputeHash() non-deterministic: %q != %q", got, gotAgain)
			}

			// Verify format: lowercase hex, 64 characters
			if len(got) != 64 || got != strings.ToLower(got) {
				t.Errorf("ComputeHash() should be 64-char lowercase hex, got %q", got)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name          string
		desired       drift.Snapshot
		actual        drift.Snapshot
		wantDrifted   bool
		wantEqualHash bool
		diffContains  []string
		diffExcludes  []string
	}{
		{
			name: "matching snapshots",
			desired: drift.Snapshot{
				ComposeYAML: "services:\n  app:\n    image: redis:7-alpine\n",
				EnvVars:     "PORT=6379\n",
			},
			actual: drift.Snapshot{
				ComposeYAML: "services:\n  app:\n    image: redis:7-alpine\n",
				EnvVars:     "PORT=6379\n",
			},
			wantDrifted:   false,
			wantEqualHash: true,
			diffContains:  nil,
			diffExcludes:  []string{"---", "+++"},
		},
		{
			name: "drifted compose",
			desired: drift.Snapshot{
				ComposeYAML: "services:\n  app:\n    image: redis:7-alpine\n",
				EnvVars:     "PORT=6379\n",
			},
			actual: drift.Snapshot{
				ComposeYAML: "services:\n  app:\n    image: redis:7.2-alpine\n",
				EnvVars:     "PORT=6379\n",
			},
			wantDrifted:   true,
			wantEqualHash: false,
			diffContains: []string{
				"--- docker-compose.yml (Desired State)",
				"+++ docker-compose.yml (Actual State)",
				"-     image: redis:7-alpine",
				"+     image: redis:7.2-alpine",
			},
			diffExcludes: []string{".env"},
		},
		{
			name: "drifted env",
			desired: drift.Snapshot{
				ComposeYAML: "services:\n  app:\n    image: redis:7-alpine\n",
				EnvVars:     "PORT=6379\n",
			},
			actual: drift.Snapshot{
				ComposeYAML: "services:\n  app:\n    image: redis:7-alpine\n",
				EnvVars:     "PORT=6380\n",
			},
			wantDrifted:   true,
			wantEqualHash: false,
			diffContains: []string{
				"--- .env (Desired State)",
				"+++ .env (Actual State)",
				"- PORT=6379",
				"+ PORT=6380",
			},
			diffExcludes: []string{"docker-compose.yml"},
		},
		{
			name: "drifted compose and env",
			desired: drift.Snapshot{
				ComposeYAML: "services:\n  app:\n    image: redis:7-alpine\n",
				EnvVars:     "PORT=6379\n",
			},
			actual: drift.Snapshot{
				ComposeYAML: "services:\n  app:\n    image: redis:7.2-alpine\n",
				EnvVars:     "PORT=6380\n",
			},
			wantDrifted:   true,
			wantEqualHash: false,
			diffContains: []string{
				"--- docker-compose.yml (Desired State)",
				"+++ docker-compose.yml (Actual State)",
				"-     image: redis:7-alpine",
				"+     image: redis:7.2-alpine",
				"--- .env (Desired State)",
				"+++ .env (Actual State)",
				"- PORT=6379",
				"+ PORT=6380",
			},
			diffExcludes: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := drift.Evaluate(tt.desired, tt.actual)

			if result.IsDrifted != tt.wantDrifted {
				t.Errorf("IsDrifted = %v, want %v", result.IsDrifted, tt.wantDrifted)
			}

			if tt.wantEqualHash {
				if result.DesiredHash != result.ActualHash {
					t.Errorf("expected hashes to match, got desired=%q actual=%q", result.DesiredHash, result.ActualHash)
				}
				if result.Diff != "" {
					t.Errorf("expected empty diff on matching snapshots, got: %q", result.Diff)
				}
			} else {
				if result.DesiredHash == result.ActualHash {
					t.Errorf("expected hashes to differ, both are %q", result.DesiredHash)
				}
			}

			for _, sub := range tt.diffContains {
				if !strings.Contains(result.Diff, sub) {
					t.Errorf("diff missing expected substring %q;\nDiff output:\n%s", sub, result.Diff)
				}
			}

			for _, sub := range tt.diffExcludes {
				if strings.Contains(result.Diff, sub) {
					t.Errorf("diff contains unexpected substring %q;\nDiff output:\n%s", sub, result.Diff)
				}
			}
		})
	}
}

func TestUnifiedDiff(t *testing.T) {
	tests := []struct {
		name         string
		nameA        string
		textA        string
		nameB        string
		textB        string
		wantEmpty    bool
		wantContains []string
	}{
		{
			name:      "identical text produces empty diff",
			nameA:     "fileA",
			textA:     "line 1\nline 2",
			nameB:     "fileB",
			textB:     "line 1\nline 2",
			wantEmpty: true,
		},
		{
			name:      "empty texts produce empty diff",
			nameA:     "fileA",
			textA:     "",
			nameB:     "fileB",
			textB:     "",
			wantEmpty: true,
		},
		{
			name:      "line modification",
			nameA:     "a.txt",
			textA:     "apple\nbanana\ncherry",
			nameB:     "b.txt",
			textB:     "apple\nblueberry\ncherry",
			wantEmpty: false,
			wantContains: []string{
				"--- a.txt",
				"+++ b.txt",
				"  apple",
				"- banana",
				"+ blueberry",
				"  cherry",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := drift.UnifiedDiff(tt.nameA, tt.textA, tt.nameB, tt.textB)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty diff, got: %q", got)
				}
				return
			}
			for _, sub := range tt.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("UnifiedDiff missing %q;\nDiff output:\n%s", sub, got)
				}
			}
		})
	}
}
