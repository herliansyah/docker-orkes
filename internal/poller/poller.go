package poller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"docker-orkes/internal/controlplane"
	"docker-orkes/internal/logger"
	"docker-orkes/internal/nodeclient"
	"docker-orkes/internal/notifier"
)

// Config configures the background poller.
type Config struct {
	Engine   controlplane.Engine
	Notifier notifier.Notifier
	Interval time.Duration
}

// Poller runs periodic health audits and edge-triggered alerts.
type Poller interface {
	Start()
	Stop()
	PollOnce(ctx context.Context) error
}

type poller struct {
	engine   controlplane.Engine
	notifier notifier.Notifier
	interval time.Duration

	mu        sync.Mutex
	nodeState map[string]string // nodeID -> "online" | "offline"
	depState  map[string]string // depID -> "running" | "failed" | "degraded"
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// New creates a new Poller.
func New(cfg Config) Poller {
	if cfg.Interval <= 0 {
		cfg.Interval = 2 * time.Minute
	}
	return &poller{
		engine:    cfg.Engine,
		notifier:  cfg.Notifier,
		interval:  cfg.Interval,
		nodeState: make(map[string]string),
		depState:  make(map[string]string),
	}
}

func (p *poller) Start() {
	p.mu.Lock()
	if p.cancel != nil {
		p.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Bound total poll cycle to interval duration to prevent hung SSH sessions from stacking
				pollCtx, pollCancel := context.WithTimeout(ctx, p.interval)
				_ = p.PollOnce(pollCtx)
				pollCancel()
			}
		}
	}()
}

func (p *poller) Stop() {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// PollOnce performs a single lightweight health audit across all nodes and deployments.
// ponytail: Uses short node ping & docker ps instead of deep drift file comparisons (ADR 0004).
func (p *poller) PollOnce(ctx context.Context) error {
	st := p.engine.Store()

	// Check if poller is explicitly disabled
	enabledSetting, _ := st.GetSetting("poller_enabled")
	if enabledSetting == "false" {
		return nil
	}

	nodes, err := st.ListNodes()
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}

	// 1. Audit Nodes
	onlineNodes := make(map[string]bool)
	for _, n := range nodes {
		// ponytail: 15s bounded timeout per node to prevent hung SSH commands or unresponsive daemon from freezing poller loop
		nodeCtx, nodeCancel := context.WithTimeout(ctx, 15*time.Second)
		dockerVer, testErr := p.engine.TestNode(nodeCtx, n.ID)
		nodeCancel()

		p.mu.Lock()
		prevState, known := p.nodeState[n.ID]
		if !known {
			prevState = n.Status
			if prevState == "" {
				prevState = "unknown"
			}
		}

		if testErr != nil {
			currState := "offline"
			p.nodeState[n.ID] = currState
			p.mu.Unlock()

			logger.Warn("poller", n.Name, "Node is unreachable", "error", testErr.Error(), "host", n.Host)

			if prevState == "online" {
				// Edge: online -> offline
				_ = p.notifier.SendAlert(ctx,
					fmt.Sprintf("Node %s is Offline", n.Name),
					fmt.Sprintf("Node %s (%s:%d) is unreachable: %v", n.Name, n.Host, n.Port, testErr),
					false,
				)
			}
		} else {
			currState := "online"
			onlineNodes[n.ID] = true
			p.nodeState[n.ID] = currState
			p.mu.Unlock()

			logger.Debug("poller", n.Name, "Node audit healthy", "docker", dockerVer)

			if prevState == "offline" {
				// Edge: offline -> online (recovery)
				logger.Info("poller", n.Name, "Node recovered online", "docker", dockerVer)
				_ = p.notifier.SendAlert(ctx,
					fmt.Sprintf("Node %s Recovered", n.Name),
					fmt.Sprintf("Node %s (%s:%d) is back online (Docker %s).", n.Name, n.Host, n.Port, dockerVer),
					true,
				)
			}
		}
	}

	// 2. Audit Deployments
	deployments, err := st.ListDeployments()
	if err != nil {
		return fmt.Errorf("list deployments: %w", err)
	}

	// Fetch telemetry containers per online node once to avoid duplicate queries
	nodeContainers := make(map[string][]nodeclient.ContainerInfo)
	for nodeID := range onlineNodes {
		telCtx, telCancel := context.WithTimeout(ctx, 15*time.Second)
		_, containers, telErr := p.engine.GetNodeTelemetry(telCtx, nodeID)
		telCancel()
		if telErr == nil {
			nodeContainers[nodeID] = containers
		}
	}

	for _, dep := range deployments {
		if !onlineNodes[dep.NodeID] {
			// Skip deployment alerting if the node itself is offline
			continue
		}

		p.mu.Lock()
		prevState, known := p.depState[dep.ID]
		if !known {
			prevState = dep.Status
			if prevState == "" {
				prevState = "unknown"
			}
		}
		p.mu.Unlock()

		containers := nodeContainers[dep.NodeID]
		var stackContainers []nodeclient.ContainerInfo
		for _, c := range containers {
			if c.MatchesStack(dep.StackName) {
				stackContainers = append(stackContainers, c)
			}
		}

		hasExited := false
		hasUnhealthy := false
		var degradedReason string
		for _, sc := range stackContainers {
			state := strings.ToLower(sc.State)
			if state == "exited" || state == "dead" {
				hasExited = true
				degradedReason = fmt.Sprintf("Container %s exited unexpectedly", sc.Names)
				break
			}
			if strings.Contains(strings.ToLower(sc.Status), "unhealthy") {
				hasUnhealthy = true
				degradedReason = fmt.Sprintf("Container %s healthcheck reported unhealthy", sc.Names)
			}
		}

		if hasExited || hasUnhealthy {
			currState := "degraded"
			p.mu.Lock()
			p.depState[dep.ID] = currState
			p.mu.Unlock()

			logger.Warn("poller", dep.NodeName, fmt.Sprintf("Deployment %s containers degraded: %s", dep.StackName, degradedReason), "stack", dep.StackName, "deployment_id", dep.ID)

			if prevState != "degraded" {
				_ = st.UpdateDeploymentStatus(dep.ID, "failed", degradedReason)
				_ = p.notifier.SendAlert(ctx,
					fmt.Sprintf("Deployment %s/%s Degraded", dep.NodeName, dep.StackName),
					degradedReason,
					false,
				)
			}
		} else if len(stackContainers) > 0 && !hasExited && !hasUnhealthy {
			currState := "running"
			p.mu.Lock()
			p.depState[dep.ID] = currState
			p.mu.Unlock()

			logger.Debug("poller", dep.NodeName, fmt.Sprintf("Deployment %s containers healthy", dep.StackName), "containers", len(stackContainers))

			if prevState == "degraded" || prevState == "failed" {
				logger.Info("poller", dep.NodeName, fmt.Sprintf("Deployment %s recovered", dep.StackName), "stack", dep.StackName)
				_ = st.UpdateDeploymentStatus(dep.ID, "running", "All containers running normally")
				_ = p.notifier.SendAlert(ctx,
					fmt.Sprintf("Deployment %s/%s Recovered", dep.NodeName, dep.StackName),
					"Containers have resumed running normally.",
					true,
				)
			}
		} else if len(stackContainers) == 0 && (prevState == "running" || dep.Status == "running") {
			currState := "degraded"
			p.mu.Lock()
			p.depState[dep.ID] = currState
			p.mu.Unlock()

			logger.Warn("poller", dep.NodeName, fmt.Sprintf("Deployment %s has no active containers", dep.StackName), "stack", dep.StackName, "deployment_id", dep.ID)

			if prevState != "degraded" {
				_ = st.UpdateDeploymentStatus(dep.ID, "failed", "No running containers found for this stack")
				_ = p.notifier.SendAlert(ctx,
					fmt.Sprintf("Deployment %s/%s Degraded", dep.NodeName, dep.StackName),
					"No running containers found for this stack on node.",
					false,
				)
			}
		} else {
			// Track regular status transitions from DB
			if dep.Status != prevState {
				p.mu.Lock()
				p.depState[dep.ID] = dep.Status
				p.mu.Unlock()

				if dep.Status == "failed" && prevState == "running" {
					_ = p.notifier.SendAlert(ctx,
						fmt.Sprintf("Deployment %s/%s Failed", dep.NodeName, dep.StackName),
						fmt.Sprintf("Deployment reported failure: %s", dep.LastLog),
						false,
					)
				} else if dep.Status == "running" && prevState == "failed" {
					_ = p.notifier.SendAlert(ctx,
						fmt.Sprintf("Deployment %s/%s Recovered", dep.NodeName, dep.StackName),
						"Deployment status is running.",
						true,
					)
				}
			}
		}
	}

	// ponytail: Prune state tracking for deleted nodes and deployments to prevent unbounded map growth
	p.mu.Lock()
	activeNodes := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		activeNodes[n.ID] = struct{}{}
	}
	for id := range p.nodeState {
		if _, exists := activeNodes[id]; !exists {
			delete(p.nodeState, id)
		}
	}

	activeDeps := make(map[string]struct{}, len(deployments))
	for _, d := range deployments {
		activeDeps[d.ID] = struct{}{}
	}
	for id := range p.depState {
		if _, exists := activeDeps[id]; !exists {
			delete(p.depState, id)
		}
	}
	p.mu.Unlock()

	return nil
}

// ParseIntervalOrDefault parses an interval string (in seconds) or returns fallback.
func ParseIntervalOrDefault(val string, fallback time.Duration) time.Duration {
	if s, err := strconv.Atoi(val); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return fallback
}
