# Progress — Sprint: `/preview` (v1.1)

> Registro contínuo do estado da sprint. Atualizar ao fim de cada sessão de trabalho.

**Sprint iniciada em:** 2026-06-17
**Última atualização:** 2026-06-17
**Status geral:** 🔄 Em andamento — Fase 4 concluída (só 4.3 smoke test pendente de VM)

---

## Estado atual

Fase 4 concluída parcialmente: wiring do subsistema de preview no `main.go` (4.1) e checklist de dependências na VM em `context.md` (4.2). `CGO_ENABLED=0 go build ./...` e `go test ./...` verdes. Tasks 4.3 (smoke test end-to-end) e 4.4 (atualização de specs/CLAUDE.md) ficam pendentes — exigem a VM/trabalho de documentação separado.

Implementação e documentação completas. Único item restante é a validação manual na VM (4.3), com roteiro pronto em [runbook.md](runbook.md): setup da VM, dependências, PATH do systemd, config e smoke test passo a passo mapeado aos 7 critérios de aceite.

**Próximo passo:** executar o smoke test 4.3 na VM seguindo o `runbook.md` e registrar os achados aqui.

---

## Fases

| Fase | Status | Notas |
| ---- | ------ | ----- |
| 1 — Fundação (schema + config + tipos) | ✅ concluída | sessão 1 |
| 2 — PreviewManager (núcleo)            | ✅ concluída | sessão 2 |
| 3 — Integração com TelegramGateway    | ✅ concluída | sessão 2 |
| 4 — Wiring e validação                 | ✅ concluída (parcial) | 4.1, 4.2 e 4.4 feitas; só 4.3 (smoke test) pendente de VM |

Legenda: ⬜ não iniciada · 🔄 em andamento · ✅ concluída · ❌ bloqueada

---

## Tarefas

### Fase 1 — Fundação

| # | Tarefa | Status | Sessão |
| - | ------ | ------ | ------ |
| 1.1 | Migration `0002_previews.sql` | ✅ | 1 |
| 1.2 | Tipos de domínio (`Preview`, `PreviewStatus`) | ✅ | 1 |
| 1.3 | Configuração (`PreviewConfig` + `config.example.yaml`) | ✅ | 1 |
| 1.4 | Queries de persistência (7 queries + teste de integração) | ✅ | 1 |

### Fase 2 — PreviewManager

| # | Tarefa | Status | Sessão |
| - | ------ | ------ | ------ |
| 2.1 | PortManager (alloc/release + teste unitário) | ✅ | 2 |
| 2.2 | Tipos internos do preview | ✅ | 2 |
| 2.3 | DevServer (por tipo de repo + healthcheck) | ✅ | 2 |
| 2.4 | Tunnel / cloudflared (start + extração de URL + crash monitor) | ✅ | 2 |
| 2.5 | PreviewManager (start/stop/status + timer de timeout) | ✅ | 2 |
| 2.6 | Startup recovery (RecoverStale) | ✅ | 2 |

### Fase 3 — TelegramGateway

| # | Tarefa | Status | Sessão |
| - | ------ | ------ | ------ |
| 3.1 | Parsing de comandos preview (+ testes unitários) | ✅ | 2 |
| 3.2 | Handlers (start / stop / status) | ✅ | 2 |
| 3.3 | Notificações novas (métodos no Gateway) | ✅ | 2 |

### Fase 4 — Wiring e validação

| # | Tarefa | Status | Sessão |
| - | ------ | ------ | ------ |
| 4.1 | Injeção no `main.go` + build `CGO_ENABLED=0` | ✅ | 3 |
| 4.2 | Checklist de dependências na VM + `context.md` | ✅ | 3 |
| 4.3 | Smoke test manual (end-to-end na VM) | 🔄 aguardando execução na VM — roteiro pronto ([runbook.md](runbook.md)) | 5 |
| 4.4 | Atualização de specs e CLAUDE.md | ✅ | 4 |

---

## Pontos abertos / decisões pendentes

| # | Ponto | Decidido em | Decisão |
| - | ----- | ----------- | ------- |
| 14.1 | `web` precisa de `uvicorn`? Checar `vite.config.js` do repo-alvo | 2026-06-17 | Flag `WebBackendEnabled` (default true) em `DevServerConfig`. Setar false se SPA puro. Ver `context.md §14.1`. |
| 14.2 | `startup_timeout_seconds=30` suficiente para Flutter Web? | — | — |
| 14.3 | Sinal de término do cloudflared: SIGTERM suficiente? | — | — |
| 14.4 | Numero da issue no comando — precisar especificar repo? | — | — |

---

## Log de sessões

| Sessão | Data | O que foi feito | Onde parou | Próximo passo |
| ------ | ---- | --------------- | ---------- | ------------- |
| 1 | 2026-06-17 | Fase 1 completa: migration 0002, tipos Preview/PreviewStatus/PreviewStopReason em domain, PreviewConfig em config, 7 queries + 2 testes de integração em store | — | Fase 2: pacote internal/preview/ |
| 2 | 2026-06-17 | Fase 2 completa: ports.go (PortManager + 4 testes), types.go (DevServerConfig + activePreview + PreviewInfo), devserver.go (DevServer.Start: web/flutter + healthcheck), tunnel.go (Tunnel.Start + extração de URL por regexp + crash monitor), manager.go (PreviewManager.Start/Stop/StatusAll/RecoverStale + timer de timeout). Decisão §14.1: flag WebBackendEnabled em DevServerConfig. context.md criado. Build e testes verdes. | — | Fase 3: TelegramGateway (parsing + handlers + notificações) |
| 2 (cont.) | 2026-06-17 | Fase 3 completa: commands.go (PreviewAction + PreviewCommand + parsePreviewCommand + 9 testes unitários), gateway.go (Gateway interface +4 métodos, gateway struct + previewMgr + workspaceBaseDir + RegisterPreviewManager, webhookHandler intercepta /preview, dispatchPreview/handlePreviewStart/Stop/Status, NotifyPreviewReady/Stopped/Replaced/Error, findIssueByNumber). Build e todos os testes verdes. | — | Fase 4: wiring main.go + smoke test |
| 5 | 2026-06-17 | Criado `runbook.md` — roteiro operacional para a task 4.3: acesso à VM, instalação de dependências (cloudflared/node/uvicorn/flutter) com validação, drop-in de PATH do systemd (ponto crítico), config da seção `preview`, pré-condições de dados (issue doing/done + checkout), smoke test em 8 passos mapeados aos 7 critérios de aceite, troubleshooting. Esclarecido que o modo rápido do cloudflared não exige conta (conta nomeada documentada como opcional/futuro). 4.3 marcada 🔄 (aguardando execução na VM). | runbook pronto | executar smoke test na VM |
| 4 | 2026-06-17 | Fase 4 (4.4): atualização de specs/docs. telegram_gateway.md §5 (comandos /preview, /preview stop, /preview status + nota de parsing/subcomandos), persistence.md §3 (tabela `previews` + nota de recovery no startup), design.md §13 (/preview movido de backlog para ✅ implementado, com decisões fechadas da sprint), CLAUDE.md (tabela Comandos do Telegram + nova seção Dependências externas na VM + entrada de backlog marcada implementada). Sem mudança de código — build/testes inalterados. | 4.4 feita | 4.3 smoke test na VM (única restante) |
| 3 | 2026-06-17 | Fase 4 (parcial): 4.1 wiring no `main.go` — bloco `if cfg.Preview.Enabled` cria PortManager + DefaultDevServerConfig + PreviewManager(store, portMgr, gw, cfg.Preview, devCfg), chama RecoverStale (erro logado, não fatal) e gw.RegisterPreviewManager(previewMgr, cfg.Workspace.BaseDir); import do pacote preview adicionado. 4.2 confirmada — seção "Dependências externas na VM" já presente no context.md. `CGO_ENABLED=0 go build ./...` e `go test ./...` verdes. | 4.1 e 4.2 feitas | 4.3 smoke test na VM (cloudflared + dev server reais); 4.4 atualizar specs/CLAUDE.md |

---

## Bloqueios e riscos

Nenhum bloqueio ativo.

**Riscos mapeados:**
- cloudflared pode ter comportamento diferente em versões distintas (URL no stderr vs. stdout). Verificar na VM antes de codificar `tunnel.go`.
- Flutter Web demora mais para inicializar que o React — o `startup_timeout_seconds` pode precisar de ajuste.
- O ponto aberto 14.1 pode adicionar complexidade ao `DevServer` para `web` se precisar de dois processos com dependência de ordem.
