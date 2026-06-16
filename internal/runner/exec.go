package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// commandSpec descreve uma invocação de subprocesso.
type commandSpec struct {
	Name  string   // ex.: "claude"
	Args  []string // flags + argumentos
	Dir   string   // working dir (checkout do repo-alvo)
	Stdin string   // conteúdo enviado por stdin (o prompt)
}

// commandResult resume o término de um subprocesso.
type commandResult struct {
	ExitCode int    // 0 em sucesso; ≠0 sinaliza erro (sem ser erro de Go)
	Stderr   string // stderr capturado (para log/diagnóstico)
}

// lineFunc recebe cada linha de stdout à medida que é lida (streaming).
type lineFunc func(line string)

// commandRunner abstrai a execução do subprocesso para permitir testes com um
// fake (sem o binário `claude` real). A implementação real é execRunner.
type commandRunner interface {
	// Run executa spec, entregando cada linha de stdout a onLine conforme chega.
	// Retorna o resultado (exit code + stderr). Um exit code ≠ 0 NÃO é um erro
	// de Go: vem em commandResult.ExitCode para o caller decidir. Erros de Go
	// são reservados para falhas de I/O / start / cancelamento.
	Run(ctx context.Context, spec commandSpec, onLine lineFunc) (commandResult, error)
}

// execRunner é a implementação real sobre os/exec.
type execRunner struct {
	// killGrace é o tempo entre SIGTERM e SIGKILL quando o ctx é cancelado
	// (timeout/shutdown). Zero usa um default conservador.
	killGrace time.Duration
}

func (e execRunner) Run(ctx context.Context, spec commandSpec, onLine lineFunc) (commandResult, error) {
	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	// Encerramento gracioso no cancelamento do ctx: SIGTERM e, após killGrace,
	// SIGKILL (via WaitDelay). exec.CommandContext já preenche Cancel com Kill;
	// sobrescrevemos para um término mais limpo.
	grace := e.killGrace
	if grace <= 0 {
		grace = 10 * time.Second
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = grace

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return commandResult{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return commandResult{}, err
	}

	// Linhas do stream-json podem ser grandes (mensagens completas): buffer
	// aumentado para evitar bufio.ErrTooLong.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if onLine != nil {
			onLine(scanner.Text())
		}
	}
	scanErr := scanner.Err()

	waitErr := cmd.Wait()
	res := commandResult{Stderr: stderr.String()}

	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		// Exit ≠ 0: reportado via ExitCode, não como erro de Go.
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if waitErr != nil {
		return res, waitErr
	}
	if scanErr != nil {
		return res, scanErr
	}
	return res, nil
}
