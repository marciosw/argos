# Context — Sprint: `/preview` (v1.1)

> Decisões tomadas durante a sprint, restrições, referências e pontos resolvidos.
> Atualizado em cada sessão de trabalho.

---

## Objetivo de negócio

Permitir que o humano inspecione visualmente o resultado do trabalho do agente no browser/celular sem precisar de ambiente local, acesso SSH à VM, nem deploy manual. Comando: `/preview #N` via Telegram → URL HTTPS temporária.

## Restrições

- Máximo 1 preview ativo (simplicidade, consumo de recursos).
- Tunelamento via cloudflared modo rápido (sem conta/token) — URL aleatória, sem autenticação.
- VM única GCP Compute Engine com disco persistente.
- Binário Go sem CGO (`CGO_ENABLED=0`).

## Decisões tomadas durante a sprint

### §14.1 — Backend uvicorn para repo `web`: flag `WebBackendEnabled` (2026-06-17)

**Contexto:** O repo `web` usa React (Vite) no frontend e Python/uvicorn no backend. Não há acesso ao `vite.config.js` do repo-alvo neste ambiente de desenvolvimento para confirmar se o frontend proxia para o backend. Duas situações possíveis:

1. `vite.config.js` tem `proxy: { '/api': 'http://localhost:<P+1>' }` → uvicorn **necessário**.
2. Frontend é SPA puro (sem proxy) → uvicorn **desnecessário** (e vai falhar ao tentar iniciar).

**Decisão:** implementar `DevServerConfig.WebBackendEnabled bool` (default `true`) em `internal/preview/types.go`. Quando `true`, o `DevServer` inicia o `uvicorn` na porta `P+1` antes do `npm run dev`. Quando `false`, pula o uvicorn.

O campo `UvicornApp string` (default `"main:app"`) permite configurar o entrypoint sem recompilar.

**Onde configurar:** via código (ao instanciar `DevServerConfig` no `main.go`), ou futuramente via `config.yaml` se necessário.

**Ação pendente (antes do smoke test):** verificar o `vite.config.js` do repo `web` e setar `WebBackendEnabled` de acordo.

---

## Referências

- [design.md](design.md) — arquitetura geral do subsistema preview.
- [tasks.md](tasks.md) — decomposição em tarefas.
- [docs/specs/design.md](../../../../docs/specs/design.md) §13 — backlog preview v1.1.
- cloudflared modo rápido: `cloudflared tunnel --url http://localhost:<PORT>` (sem conta).

## Dependências externas na VM

Ver design.md §13. Resumo para o smoke test (Fase 4):

```bash
# cloudflared
curl -L --output cloudflared.deb \
  https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb
sudo dpkg -i cloudflared.deb

# node/npm (se não instalado)
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt-get install -y nodejs

# flutter (se não instalado)
# https://docs.flutter.dev/get-started/install/linux
```

Todos precisam estar no `PATH` do processo Argos (configurar `Environment=` no unit systemd).
