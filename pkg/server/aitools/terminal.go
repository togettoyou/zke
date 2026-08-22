package aitools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/clusterterminal"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

type terminalCommandArguments struct {
	Command string `json:"command"`
}

// terminalTurn owns the immutable permission snapshot and terminal Pod shared
// by all run_terminal_command calls in exactly one AIOps turn. ready lets the
// lifecycle remain correct if creation and turn cleanup ever overlap.
type terminalTurn struct {
	ready       chan struct{}
	userID      string
	clusterID   string
	permissions []string
	session     clusterterminal.CommandSession
	err         error
	context     context.Context
	cancel      context.CancelFunc
	revoked     chan struct{}
	revokeOnce  sync.Once
	closeOnce   sync.Once
	closeDone   chan struct{}
	closeErr    error
}

func (catalogue *Catalogue) runTerminalCommand(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments terminalCommandArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	arguments.Command = strings.TrimSpace(arguments.Command)
	if arguments.Command == "" || len(arguments.Command) > agentprotocol.MaxPodExecCommandBytes ||
		strings.TrimSpace(invocation.TurnID) == "" {
		return airuntime.ToolResult{}, fmt.Errorf("%w: command 或 Turn ID 不能为空", airuntime.ErrInvalidInput)
	}

	terminal, err := catalogue.acquireTerminalTurn(ctx, invocation)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		_ = catalogue.closeTerminalTurn(context.WithoutCancel(ctx), terminal)
		return airuntime.ToolResult{}, err
	}
	if terminal.closed() && !terminal.permissionRevoked() {
		return airuntime.ToolResult{
			Text:   "本轮 Cluster Terminal 已因先前的传输或会话故障关闭；请在下一轮重新执行命令。",
			Failed: true,
		}, nil
	}
	if terminal.permissionRevoked() {
		_ = catalogue.closeTerminalTurn(ctx, terminal)
		return terminalPermissionFailure(clusterterminal.CommandResult{}), nil
	}
	if err := catalogue.revalidateTerminalPermissions(
		ctx, invocation.UserID, invocation.ClusterID, terminal.permissions,
	); err != nil {
		terminal.markRevoked()
		_ = catalogue.closeTerminalTurn(ctx, terminal)
		if errors.Is(err, rbac.ErrDenied) {
			return terminalPermissionFailure(clusterterminal.CommandResult{}), nil
		}
		return airuntime.ToolResult{}, err
	}

	commandContext, cancelCommand := context.WithCancel(terminal.context)
	defer cancelCommand()
	result, commandErr := catalogue.dependencies.Terminal.ExecuteCommand(
		commandContext,
		clusterterminal.CommandInput{Session: terminal.session, Command: arguments.Command},
	)
	// Close the monitor only after one final no-cache check. This covers short
	// commands that finish before the first periodic revalidation tick.
	if commandErr == nil && !terminal.permissionRevoked() {
		if err := catalogue.revalidateTerminalPermissions(
			commandContext, invocation.UserID, invocation.ClusterID, terminal.permissions,
		); err != nil {
			terminal.markRevoked()
		}
	}
	if terminal.permissionRevoked() {
		_ = catalogue.closeTerminalTurn(ctx, terminal)
		return terminalPermissionFailure(result), nil
	}
	if commandErr != nil {
		// A transport-level error leaves the remote exec state uncertain. Retire
		// the Pod so a later model step cannot keep using a suspect session.
		_ = catalogue.closeTerminalTurn(ctx, terminal)
		return airuntime.ToolResult{}, commandErr
	}

	text := formatTerminalCommandResult(result)
	if result.OutputLimitReached {
		text += "\n输出达到上限，后续内容已停止读取。请改用更精确的命令缩小结果。"
	}
	return airuntime.ToolResult{
		Text:   catalogue.prune(text),
		Failed: result.ExitCode != 0 || result.OutputLimitReached,
	}, nil
}

func (catalogue *Catalogue) acquireTerminalTurn(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
) (*terminalTurn, error) {
	catalogue.mu.Lock()
	terminal, exists := catalogue.terminals[invocation.TurnID]
	if exists {
		if terminal.userID != invocation.UserID || terminal.clusterID != invocation.ClusterID {
			catalogue.mu.Unlock()
			return nil, fmt.Errorf("%w: Turn 终端作用域不一致", airuntime.ErrInvalidInput)
		}
		catalogue.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-terminal.ready:
			return terminal, terminal.err
		}
	}
	terminal = &terminalTurn{
		ready: make(chan struct{}), userID: invocation.UserID, clusterID: invocation.ClusterID,
		revoked: make(chan struct{}), closeDone: make(chan struct{}),
	}
	catalogue.terminals[invocation.TurnID] = terminal
	catalogue.mu.Unlock()

	permissions, err := catalogue.resolveTerminalPermissions(ctx, invocation)
	if err == nil {
		terminal.permissions = append([]string(nil), permissions...)
		terminal.session, err = catalogue.dependencies.Terminal.CreateCommandSession(
			ctx,
			clusterterminal.CommandSessionInput{
				UserID: invocation.UserID, ClusterID: invocation.ClusterID,
				IdempotencyKey: invocation.TurnID, Permissions: permissions,
			},
		)
		if err == nil {
			terminal.context, terminal.cancel = context.WithCancel(ctx)
			go catalogue.monitorTerminalTurn(terminal)
		}
	}
	catalogue.mu.Lock()
	terminal.err = err
	close(terminal.ready)
	catalogue.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return terminal, nil
}

func (catalogue *Catalogue) resolveTerminalPermissions(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
) ([]string, error) {
	permissions := make([]string, 0, len(rbac.Permissions()))
	authorizationContext := rbac.WithoutBindingCache(ctx)
	for _, permission := range rbac.Permissions() {
		if !strings.HasPrefix(string(permission), "cluster.") {
			continue
		}
		_, err := catalogue.dependencies.Permissions.AuthorizeCluster(
			authorizationContext, invocation.UserID, permission, invocation.ClusterID,
		)
		if err == nil {
			// Arbitrary terminal output is persisted and sent to the configured
			// model, so Secret access is never projected. Agent Namespace manage
			// is also excluded to prevent exec into the credential-proxy container.
			if permission != rbac.PermissionClusterSecretRead &&
				permission != rbac.PermissionClusterSecretManage &&
				permission != rbac.PermissionClusterAgentNamespaceManage {
				permissions = append(permissions, string(permission))
			}
			continue
		}
		if !errors.Is(err, rbac.ErrDenied) {
			return nil, err
		}
	}
	return permissions, nil
}

func (catalogue *Catalogue) revalidateTerminalPermissions(
	ctx context.Context,
	userID string,
	clusterID string,
	permissions []string,
) error {
	authorizationContext := rbac.WithoutBindingCache(ctx)
	for _, value := range permissions {
		if _, err := catalogue.dependencies.Permissions.AuthorizeCluster(
			authorizationContext, userID, rbac.Permission(value), clusterID,
		); err != nil {
			return err
		}
	}
	return nil
}

func (catalogue *Catalogue) monitorTerminalTurn(
	terminal *terminalTurn,
) {
	ticker := time.NewTicker(catalogue.config.TerminalRevalidate)
	defer ticker.Stop()
	for {
		select {
		case <-terminal.context.Done():
			_ = catalogue.closeTerminalTurn(context.WithoutCancel(terminal.context), terminal)
			return
		case <-ticker.C:
			if err := catalogue.revalidateTerminalPermissions(
				terminal.context, terminal.userID, terminal.clusterID, terminal.permissions,
			); err != nil {
				terminal.markRevoked()
				_ = catalogue.closeTerminalTurn(context.WithoutCancel(terminal.context), terminal)
				return
			}
		}
	}
}

func terminalPermissionFailure(result clusterterminal.CommandResult) airuntime.ToolResult {
	return airuntime.ToolResult{
		Text: formatTerminalCommandResult(result) +
			"\n执行前或执行期间权限重验失败，本轮终端已关闭；已经发生的操作不会自动回滚。",
		Failed: true,
	}
}

func (terminal *terminalTurn) markRevoked() {
	terminal.revokeOnce.Do(func() {
		close(terminal.revoked)
		if terminal.cancel != nil {
			terminal.cancel()
		}
	})
}

func (terminal *terminalTurn) permissionRevoked() bool {
	select {
	case <-terminal.revoked:
		return true
	default:
		return false
	}
}

func (terminal *terminalTurn) closed() bool {
	select {
	case <-terminal.closeDone:
		return true
	default:
		return false
	}
}

// CloseTurn implements airuntime.TurnScopedToolSet. Cleanup is single-owner and
// the map entry remains visible until deletion completes, so a concurrent
// permission-revocation cleanup is joined rather than mistaken for completion.
func (catalogue *Catalogue) CloseTurn(ctx context.Context, turnID string) error {
	catalogue.mu.Lock()
	terminal, exists := catalogue.terminals[turnID]
	catalogue.mu.Unlock()
	if !exists {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-terminal.ready:
	}
	if terminal.err != nil {
		catalogue.mu.Lock()
		if catalogue.terminals[turnID] == terminal {
			delete(catalogue.terminals, turnID)
		}
		catalogue.mu.Unlock()
		return nil
	}
	err := catalogue.closeTerminalTurn(ctx, terminal)
	catalogue.mu.Lock()
	if catalogue.terminals[turnID] == terminal {
		delete(catalogue.terminals, turnID)
	}
	catalogue.mu.Unlock()
	return err
}

func (catalogue *Catalogue) closeTerminalTurn(
	ctx context.Context,
	terminal *terminalTurn,
) error {
	terminal.closeOnce.Do(func() {
		if terminal.cancel != nil {
			terminal.cancel()
		}
		terminal.closeErr = catalogue.dependencies.Terminal.FinishCommandSession(ctx, terminal.session)
		close(terminal.closeDone)
	})
	<-terminal.closeDone
	return terminal.closeErr
}

func formatTerminalCommandResult(result clusterterminal.CommandResult) string {
	return fmt.Sprintf("命令退出码：%d\nstdout:\n%s\nstderr:\n%s",
		result.ExitCode, result.Stdout, result.Stderr)
}
