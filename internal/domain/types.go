// Package domain contém os tipos compartilhados entre os componentes do
// orquestrador (store, config, scheduler, github, telegram, runner). Por
// convenção, este pacote NÃO importa nenhum outro pacote interno (evita
// dependência circular): é a base do grafo de dependências.
package domain

import "time"

// Repositórios gerenciados (ver design.md §1).
const (
	RepoWeb    = "web"
	RepoMobile = "mobile"
	RepoHybrid = "hybrid"
)

// Repos é a lista canônica dos repositórios suportados.
var Repos = []string{RepoWeb, RepoMobile, RepoHybrid}

// IsValidRepo informa se r é um dos repositórios suportados.
func IsValidRepo(r string) bool {
	for _, v := range Repos {
		if v == r {
			return true
		}
	}
	return false
}

// Phase é o estado OPERACIONAL de uma issue no orquestrador (persistido no
// SQLite). Note que `awaiting_approval` é um estado operacional interno: no
// GitHub ele permanece sob o label `documentation` (ver persistence.md §3).
type Phase string

const (
	PhaseReady            Phase = "ready"
	PhaseDocumentation    Phase = "documentation"
	PhaseAwaitingApproval Phase = "awaiting_approval"
	PhaseTodo             Phase = "todo"
	PhaseDoing            Phase = "doing"
	PhaseDone             Phase = "done"
	PhaseError            Phase = "error"
)

// Labels do GitHub (fonte de verdade do estado de NEGÓCIO). Mutuamente
// exclusivas para as fases; `LabelError` é avulsa (ver github_poller.md §4).
const (
	LabelReady         = "agent:ready"
	LabelDocumentation = "documentation"
	LabelTodo          = "todo"
	LabelDoing         = "doing"
	LabelDone          = "done"
	LabelError         = "agent:error"
)

// Issue é o registro operacional de uma issue conhecida pelo orquestrador
// (espelha a tabela `issues`). O corpo/labels brutos vêm do GitHub e vivem no
// componente github; aqui guardamos apenas o estado operacional.
type Issue struct {
	ID        int64
	Repo      string
	Number    int
	Title     string
	Phase     Phase
	SpecDir   string
	GitHubURL string
	PRURL     string
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SessionStatus é o ciclo de vida de uma sessão do Claude Code.
type SessionStatus string

const (
	SessionRunning   SessionStatus = "running"
	SessionCompleted SessionStatus = "completed"
	SessionFailed    SessionStatus = "failed"
	SessionResumed   SessionStatus = "resumed"
)

// Session é uma execução do subprocesso `claude` (espelha a tabela `sessions`).
type Session struct {
	ID              int64
	IssueID         int64
	Phase           Phase
	ClaudeSessionID string
	Model           string
	Status          SessionStatus
	ContextPct      float64 // último uso de janela observado (0..1)
	ResumedFrom     int64   // 0 quando não é retomada
	StartedAt       time.Time
	EndedAt         time.Time // zero enquanto a sessão está aberta
}

// RepoState é o estado por repositório (espelha a tabela `repo_state`).
type RepoState struct {
	Repo         string
	Paused       bool
	PausedReason string
	LastPolledAt time.Time
	ETag         string
	UpdatedAt    time.Time
}

// ApprovalState é o estado de uma aprovação na fila de validação humana.
type ApprovalState string

const (
	ApprovalPending  ApprovalState = "pending"
	ApprovalApproved ApprovalState = "approved"
	ApprovalRejected ApprovalState = "rejected"
)

// Approval é uma entrada da fila de aprovações (espelha a tabela `approvals`).
type Approval struct {
	ID          int64
	IssueID     int64
	State       ApprovalState
	Reason      string
	RequestedAt time.Time
	DecidedAt   time.Time
	DecidedBy   int64 // chat_id do Telegram que decidiu
}

// Lock é um lock de processamento por issue (espelha a tabela `locks`).
type Lock struct {
	IssueID    int64
	Holder     string
	AcquiredAt time.Time
	ExpiresAt  time.Time
}

// CommandType é o tipo de um comando recebido via Telegram.
type CommandType string

const (
	CmdApprove CommandType = "approve"
	CmdReject  CommandType = "reject"
	CmdPause   CommandType = "pause"
	CmdResume  CommandType = "resume"
	CmdStatus  CommandType = "status"
	CmdRun     CommandType = "run"
)

// Command é um comando do Telegram já parseado, pronto para o Scheduler.
type Command struct {
	Type   CommandType
	Repo   string // para pause/resume
	Issue  int    // para approve/reject/run
	Reason string // para reject (texto livre)
	ChatID int64
}

// ModelConfig é o resultado da resolução de modelo para um repo+issue,
// produzido pelo ConfigManager (ver model_config.md §5).
type ModelConfig struct {
	Model            string  // id ou alias repassado ao --model do claude
	ContextWindow    int     // tokens da janela do modelo (para cálculo de uso)
	ContextThreshold float64 // limiar de retomada (0..1)
}

// Task é a unidade de trabalho despachada ao TaskRunner (ver task_runner.md §8).
type Task struct {
	Repo          string
	Issue         int
	Phase         Phase  // fase em que a tarefa será executada (documentation ou doing)
	RepoPath      string // checkout local do repo-alvo
	Model         string // resolvido pelo ConfigManager
	ContextWindow int    // janela do modelo (tokens)
}

// PreviewStatus é o ciclo de vida de um preview (espelha a tabela `previews`).
type PreviewStatus string

const (
	PreviewStarting PreviewStatus = "starting"
	PreviewRunning  PreviewStatus = "running"
	PreviewStopping PreviewStatus = "stopping"
	PreviewStopped  PreviewStatus = "stopped"
	PreviewDead     PreviewStatus = "dead"
)

// PreviewStopReason descreve por que um preview foi encerrado.
type PreviewStopReason string

const (
	PreviewStopTimeout  PreviewStopReason = "timeout"
	PreviewStopCommand  PreviewStopReason = "command"
	PreviewStopReplaced PreviewStopReason = "replaced"
	PreviewStopCrash    PreviewStopReason = "crash"
	PreviewStopRestart  PreviewStopReason = "restart"
)

// Preview é um registro de preview de aplicação (espelha a tabela `previews`).
type Preview struct {
	ID         int64
	IssueID    int64
	Repo       string
	Port       int
	ExtraPort  int // porta adicional (ex.: uvicorn para repo web); 0 se não usada
	TunnelURL  string
	Status     PreviewStatus
	StartedAt  time.Time
	StoppedAt  time.Time // zero enquanto ativo
	StopReason PreviewStopReason
}

// AuditEntry é uma linha do log de auditoria (espelha a tabela `audit_log`).
type AuditEntry struct {
	ID        int64
	IssueID   int64 // 0 quando não associado a uma issue
	Repo      string
	Event     string
	Detail    string // JSON livre
	CreatedAt time.Time
}

// StatusSnapshot é a visão agregada para o comando /status (ver
// persistence.md §4: StatusSnapshot).
type StatusSnapshot struct {
	Repos          []RepoState
	PhaseCounts    map[Phase]int
	ActiveSessions int
}
