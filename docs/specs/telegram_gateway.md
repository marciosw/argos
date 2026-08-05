# Spec — TelegramGateway

> Gateway de comando e validação via Telegram Bot API (modo **webhook**): recebe comandos, envia digests e arquivos, e gerencia a fila de aprovações pendentes — para que o humano valide tudo pelo celular.

**Status:** rascunho para validação.

---

## 1. Responsabilidades

1. Expor um endpoint HTTPS de **webhook** que recebe updates do Telegram.
2. Parsear e despachar comandos (`/approve`, `/reject`, `/pause`, `/resume`, `/status`, `/run`).
3. Enviar mensagens formatadas (digest da spec, notificações de PR, erros).
4. Enviar arquivos `.md` como **anexos** (abríveis no celular).
5. Manter o vínculo entre comando do humano e estado da issue (fila de aprovação) via SQLite.
6. Autorizar: aceitar comandos apenas de chat(s)/usuário(s) autorizados.

## 2. Webhook vs. long polling

- Requisito: **webhook**. O Telegram faz `POST` num endpoint HTTPS público com certificado válido.
- **Hospedagem definida (2026-06-15):** **VM única no GCP Compute Engine**. O Argos roda inteiro nessa VM — webhook **na mesma VM**, sem separação em instâncias distintas.
- **TLS:** gerenciado por **Caddy** (Let's Encrypt automático) como reverse proxy na frente do servidor HTTP do Argos (`:8080`).
- **Topologia:** `Internet → Caddy → :8080 (servidor HTTP do Argos)`. A rota `/telegram/webhook` aponta para o servidor interno.
- Registro do webhook no startup via `setWebhook` (URL pública da VM, atendida pelo Caddy, + `secret_token`).
- Telegram envia o header `X-Telegram-Bot-Api-Secret-Token`; o gateway **valida** esse segredo em cada request (rejeita se não bater).

### Operação na VM única

Por rodar numa VM dedicada e sempre ligada, **não há o problema de filesystem efêmero**: o disco da VM é persistente por padrão, então SQLite e os checkouts dos repos-alvo vivem em disco local normal (ver `persistence.md`). Pontos operacionais:

1. **Processo único, sempre ligado**: o orquestrador (Scheduler em loop + servidor HTTP do webhook + subprocessos `claude`) é um só processo na VM. Gerenciar via `systemd` (restart automático).
2. **Dependências na VM**: Claude Code CLI e `git` instalados no host.
3. **Instância única**: como há só uma VM/processo, não existe escala horizontal duplicando o Scheduler; os locks do SQLite (`persistence.md`) seguem como salvaguarda.

### Caddy (reverse proxy + TLS)

Caddy provê HTTPS com renovação automática de certificado (Let's Encrypt) sem configuração manual. Caddyfile mínimo:

```caddyfile
argos.exemplo.com {
    reverse_proxy localhost:8080
}
```

- O domínio (`argos.exemplo.com`) deve apontar (DNS A/AAAA) para o IP público da VM.
- Caddy obtém e renova o certificado automaticamente; não é preciso cron/manual.
- A porta `:8080` (servidor HTTP do Argos) não precisa ser exposta diretamente — só o Caddy (`:443`/`:80`) fica público.

## 3. Configuração

```yaml
telegram:
  bot_token_env: "TELEGRAM_BOT_TOKEN"     # token do bot (via env)
  webhook_url: "https://<host>/telegram/webhook"
  secret_token_env: "TELEGRAM_WEBHOOK_SECRET"
  allowed_chat_ids: [123456789]           # chats/usuários autorizados a comandar
```

- `bot_token` e `secret_token` **sempre via env**, nunca versionados.
- `allowed_chat_ids`: lista branca. Updates de chats fora da lista são descartados (logados).

## 4. Servidor HTTP

- `net/http` com uma rota `POST /telegram/webhook`.
- Validação em ordem: (1) secret token header; (2) `chat.id` autorizado; (3) parse do update.
- Responde `200 OK` rápido; o processamento pesado (envio de resposta, mutação de estado) acontece assíncrono para não estourar o timeout do Telegram. Cada update é **idempotente** por `update_id` (gravado no SQLite).

## 5. Comandos

| Comando            | Sintaxe              | Efeito |
| ------------------ | -------------------- | ------ |
| Aprovar spec       | `/approve #N`        | Marca a spec da issue N como aprovada → Scheduler move label para `todo`. |
| Rejeitar spec      | `/reject #N motivo`  | Rejeita a spec; agente volta a `documentation` com `motivo` gravado em `context.md`. |
| Pausar repo        | `/pause repo`        | `repo ∈ {web, mobile, hybrid}`. Pausa polling/processamento do repo. |
| Retomar repo       | `/resume repo`       | Retoma o repo pausado. |
| Status             | `/status`            | Resumo de todos os repos e issues em andamento. |
| Executar agora     | `/run #N`            | Força processamento imediato da issue N (bypass do ciclo de polling). |
| Preview            | `/preview #N`        | Sobe o servidor de dev do repo da issue N + túnel cloudflared; retorna URL HTTPS temporária. Substitui o preview anterior, se houver. |
| Parar preview      | `/preview stop #N`   | Derruba o servidor de dev e o túnel da issue N. |
| Status do preview  | `/preview status`    | Lista o preview ativo (URL e tempo restante). |

### Parsing

- Formato: `/comando arg1 arg2...`. Aceitar a sintaxe `@botname` (ex.: `/approve@meu_bot #42`).
- `#N`: aceitar com ou sem `#` (`/approve 42` == `/approve #42`).
- `motivo` em `/reject`: tudo após o número é o motivo (texto livre, pode ter espaços).
- `repo` em `/pause`/`/resume`: validar contra `{web, mobile, hybrid}`; responder erro amigável se inválido.
- `/preview`: subcomandos `stop` e `status`. `/preview #N` (start) e `/preview stop #N` exigem `#N`; `/preview status` não recebe argumento. Implementado em `internal/preview/` — detalhes em [docs/specs/sprints/preview-v1.1/design.md](sprints/preview-v1.1/design.md).
- Comando desconhecido / argumentos faltando → mensagem de ajuda contextual.

### Validações de estado

- `/approve #N` / `/reject #N`: a issue N precisa estar em estado `awaiting_approval` no SQLite. Caso contrário, responder explicando o estado atual.
- `/run #N`: enfileira a issue para o próximo passo elegível imediatamente; se já houver sessão ativa, responder que já está em execução.

## 6. Validação via Telegram — fim da documentação

Quando o TaskRunner conclui a fase `documentation`, o gateway executa:

1. **Digest** (mensagem formatada, MarkdownV2 ou HTML — **a validar**, sugestão HTML por ser mais tolerante a escaping):
   - Título e número da issue.
   - **Objetivos** (do `design.md`).
   - **Componentes** envolvidos.
   - **Decisões técnicas** principais.
   - **Pontos de atenção** / riscos.
   - Instrução de ação: responder `/approve #N` ou `/reject #N motivo`.
2. **Anexos**: envia `design.md`, `tasks.md`, `context.md` (e opcionalmente `progress.md`) via `sendDocument`, para leitura no celular.
3. Grava no SQLite o estado `awaiting_approval` com referência aos arquivos enviados (fila de aprovação).

> O digest é gerado a partir das specs já escritas (resumo estruturado), não é o conteúdo bruto dos arquivos — os arquivos vão como anexo para quem quiser o detalhe.

## 7. Notificações de saída (orquestrador → humano)

| Evento                         | Mensagem |
| ------------------------------ | -------- |
| Documentação pronta            | digest + anexos (seção 6) |
| Codificação concluída / PR     | "✅ Issue #N (repo): PR aberto → `<link>`" |
| Erro em uma tarefa             | "⚠️ Issue #N (repo): falhou na fase X — `<resumo do erro>`" |
| Repo pausado/retomado          | confirmação do comando |
| Retomada por contexto (>65%)   | (opcional) "🔄 Issue #N: sessão reiniciada por uso de contexto" |

## 8. Envio de arquivos

- `sendDocument` (multipart) para cada `.md`. Caption curta identificando o arquivo.
- Limite de tamanho do Telegram (bots: até 50 MB) é folgado para `.md`; sem preocupação prática.
- Em caso de falha de envio de um anexo, ainda enviar o digest e logar o anexo que faltou.

## 9. API interna do componente

```go
type Gateway interface {
    // Inicia o servidor HTTP e registra o webhook.
    Start(ctx) error

    // Envia o digest + anexos para a fila de aprovação.
    SendApprovalRequest(ctx, in ApprovalRequest) error

    // Notificações genéricas.
    Notify(ctx, chatID int64, msg string) error
    NotifyPR(ctx, issue Ref, prURL string) error
    NotifyError(ctx, issue Ref, phase string, err error) error
}

// Comandos recebidos são publicados para o Scheduler:
type Command struct {
    Type   CommandType // Approve, Reject, Pause, Resume, Status, Run
    Repo   string      // para pause/resume
    Issue  int         // para approve/reject/run
    Reason string      // para reject
    ChatID int64
}
```

A comunicação gateway→scheduler é por canal/queue + persistência no SQLite (o comando é gravado antes de confirmar ao usuário, garantindo durabilidade).

## 10. Segurança

- Token e secret via env.
- Lista branca de `chat_id`.
- Validação do `secret_token` em todo request do webhook.
- Rate limiting básico por chat para evitar flood.
- Nunca ecoar segredos nas mensagens/logs.

## 11. Pontos abertos para validação

1. **Formato das mensagens**: HTML vs. MarkdownV2 (sugestão: HTML).
2. **Anexos**: enviar também `progress.md`? (sugestão: só na conclusão de doc enviar design/tasks/context).
3. **Biblioteca**: cliente Telegram próprio sobre `net/http` vs. lib (ex.: `go-telegram/bot`). Sugestão: cliente mínimo próprio.
4. Múltiplos aprovadores ou apenas um `chat_id`?

## 12. Backlog

### Conversa livre com o agente via Telegram (texto + imagens) — prioridade v1.2 (registrado 2026-06-15)

> Média prioridade. Alvo: **v1.2**. Visão geral em `design.md` §13.

**Descrição:** permitir que o humano envie mensagens de texto livre e imagens (prints de UI) diretamente pelo Telegram como instruções para o agente, sem precisar estruturar um comando formal.

**Exemplos de uso:**
- Enviar um print de tela e escrever "alinha o botão de submit com o campo de email".
- Enviar "muda a cor do header para o tom de laranja do design system".
- Enviar um print de erro e escrever "corrige esse bug".

**Comportamento esperado (no gateway):**
- Mensagens fora do padrão `/comando` são tratadas como **instruções livres** (hoje o gateway só parseia comandos — ver §5).
- Imagens enviadas via Telegram são recebidas e repassadas como **contexto visual (vision)** para o Claude Code na sessão ativa ou na próxima sessão.
- Com mais de uma task ativa, o gateway **pergunta para qual issue/repo** a instrução se destina antes de repassar (roteamento de contexto).
- A instrução é registrada no `context.md` da issue correspondente para rastreabilidade.

**Motivação:** reduzir fricção nas correções rápidas de UI e bugs simples — sem abrir o GitHub, criar issue formal e aplicar labels para pequenos ajustes visuais.

**Requisitos técnicos a resolver na implementação:**
- Download da imagem via Telegram Bot API (`getFile` + download).
- Repasse da imagem ao Claude Code com suporte a vision (**confirmar suporte no `stream-json`/CLI antes de implementar**).
- Roteamento de instrução quando múltiplas tasks estão ativas (menu de seleção no Telegram ou comando `/para #N <mensagem>`).
- Gestão de sessão: se não houver sessão ativa para o repo, enfileirar a instrução para a próxima execução via `context.md`.
