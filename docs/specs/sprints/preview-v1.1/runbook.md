# Runbook — `/preview` na VM (setup + smoke test 4.3)

> Roteiro operacional para preparar a VM, configurar o tunelamento e executar o
> smoke test end-to-end da feature `/preview` (sprint v1.1, task 4.3).
>
> **Alvo:** VM única no GCP Compute Engine (Debian/Ubuntu), conforme `CLAUDE.md`.
> **Pré-requisito:** Argos já buildado e rodando via systemd (sessão 8 — `deploy/`).

---

## 0. TL;DR — o que você vai fazer

1. Instalar as dependências externas na VM: `cloudflared`, `node`/`npm`, `flutter`, `python`/`uvicorn`.
2. Garantir que todas estão no **PATH do processo systemd do Argos** (passo mais sujeito a erro).
3. Ajustar `config.yaml` (seção `preview`) e reiniciar o serviço.
4. Deixar pelo menos um repo em checkout no workspace, com uma issue em `doing`/`done`.
5. Rodar a sequência de smoke test pelo Telegram e validar cada critério de aceite.

> ⚠️ **Sobre a conta Cloudflare:** o Argos usa o **modo rápido** do cloudflared
> (`cloudflared tunnel --url ...`), que **NÃO exige conta, login nem token**. A URL
> é gerada em `*.trycloudflare.com`. A criação de conta só é necessária se você
> quiser, no futuro, túneis nomeados com URL estável — ver §6 (opcional).

---

## 1. Acesso à VM

```bash
# via gcloud (recomendado)
gcloud compute ssh argos-vm --zone=<sua-zona>

# ou SSH direto
ssh <usuario>@<ip-da-vm>
```

Confirme quem roda o Argos (o unit `deploy/argos.service` não define `User=`, então
roda como `root` por padrão — os comandos abaixo assumem isso; ajuste se você
configurou um usuário dedicado):

```bash
systemctl status argos
sudo systemctl cat argos      # mostra o unit efetivo (ExecStart, EnvironmentFile, etc.)
```

---

## 2. Instalar dependências externas

### 2.1 cloudflared (tunelamento — obrigatório)

```bash
curl -L --output /tmp/cloudflared.deb \
  https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb
sudo dpkg -i /tmp/cloudflared.deb

# valida
cloudflared --version
which cloudflared            # esperado: /usr/bin/cloudflared ou /usr/local/bin/cloudflared
```

> Para ARM (ex.: VM `t2a`), troque `amd64` por `arm64`.

### 2.2 node / npm (obrigatório para o repo `web`)

```bash
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt-get install -y nodejs

node --version && npm --version
which npm                    # esperado: /usr/bin/npm
```

### 2.3 Python + uvicorn (obrigatório para o backend do repo `web`)

O Argos chama `uvicorn main:app` (configurável). O `uvicorn` precisa estar
resolvível no PATH do Argos. Duas abordagens:

```bash
# A) instalação global (mais simples para a VM dedicada)
sudo apt-get install -y python3 python3-pip
sudo pip3 install --break-system-packages uvicorn   # ou em venv (ver nota)

which uvicorn               # esperado: /usr/local/bin/uvicorn ou /usr/bin/uvicorn
```

> **Nota:** se o repo `web` usa um venv próprio, o `uvicorn` global pode não ter as
> dependências da app. Nesse caso, ou (a) instale as deps da app no Python global,
> ou (b) deixe o backend desligado e use só o frontend — ver §4.2 (`WebBackendEnabled=false`).

### 2.4 Flutter (obrigatório para `mobile`/`hybrid`)

```bash
# via snap (mais simples)
sudo snap install flutter --classic
flutter --version

# OU instalação manual em /opt
# git clone https://github.com/flutter/flutter.git -b stable /opt/flutter
# /opt/flutter/bin/flutter --version

which flutter              # anote o caminho — provavelmente NÃO está no PATH do systemd
```

> ⚠️ Flutter quase nunca cai no PATH padrão do systemd (`/usr/bin:/bin`). O snap
> coloca em `/snap/bin/flutter`; a instalação manual em `/opt/flutter/bin`. Isso é
> tratado no §3.

### 2.5 Teste rápido do cloudflared (antes do smoke test — task 4.2)

```bash
# sobe um servidor http qualquer numa porta do range e tuneliza
python3 -m http.server 9000 &
cloudflared tunnel --url http://localhost:9000
# Esperado no stderr: uma linha "https://<aleatorio>.trycloudflare.com"
# Abra a URL no celular → deve listar o diretório. Ctrl+C para encerrar ambos.
kill %1
```

Se isso funcionar, o tunelamento está OK independentemente do Argos.

---

## 3. PATH do systemd — o ponto crítico

O Argos roda os dev servers e o cloudflared como **subprocessos que herdam o
ambiente do processo systemd**. O PATH de um serviço systemd é mínimo
(`/usr/local/bin:/usr/bin:/bin`) e **não inclui** `/snap/bin` nem `/opt/flutter/bin`.
Se um binário não for encontrado, o preview falha com `exec: "flutter": executable file not found in $PATH`.

### 3.1 Descobrir os caminhos reais

```bash
for b in cloudflared npm node flutter uvicorn python3; do
  printf '%-12s %s\n' "$b" "$(command -v $b || echo 'NAO ENCONTRADO')"
done
```

### 3.2 Adicionar os diretórios ao unit do Argos

Crie um drop-in (não edite o unit original):

```bash
sudo systemctl edit argos
```

No editor que abrir, insira (ajuste os caminhos conforme o §3.1):

```ini
[Service]
Environment=PATH=/usr/local/bin:/usr/bin:/bin:/snap/bin:/opt/flutter/bin
```

Salve e recarregue:

```bash
sudo systemctl daemon-reload
sudo systemctl restart argos
sudo systemctl show argos -p Environment    # confirma o PATH efetivo
```

> Alternativa: criar symlinks em `/usr/local/bin` (ex.:
> `sudo ln -s /snap/bin/flutter /usr/local/bin/flutter`). O drop-in de PATH é mais limpo.

---

## 4. Configuração do Argos

### 4.1 Seção `preview` no `config.yaml`

Edite `/etc/argos/config.yaml` (caminho do `ExecStart` no unit). A seção `preview`
(ver `config.example.yaml`):

```yaml
preview:
  enabled: true
  port_range_start: 9000        # primeira porta do pool de dev servers
  port_range_end: 9099          # última (inclusive) — 100 portas
  timeout_minutes: 30           # preview expira sozinho após N min
  startup_timeout_seconds: 30   # tempo máx p/ dev server + túnel ficarem prontos
  cloudflared_bin: "cloudflared"  # ou caminho absoluto p/ não depender do PATH
```

> 💡 Para eliminar o risco do PATH no caso do cloudflared, você pode setar
> `cloudflared_bin: "/usr/bin/cloudflared"`. Os dev servers (npm/flutter/uvicorn)
> **continuam dependendo do PATH** do §3 — não há override de caminho para eles.

> ⏱️ `startup_timeout_seconds: 30` pode ser curto para o **primeiro** boot do
> Flutter Web (compilação) ou um `npm install` frio. Se o smoke test falhar com
> timeout, suba para `90` e reinicie. (Ponto aberto §14.2 da sprint.)

### 4.2 Backend do repo `web` (flag `WebBackendEnabled`)

Hoje o default é `true` (Argos sobe `uvicorn main:app` na porta extra antes do
`npm run dev`). Se o frontend do repo `web` for SPA puro (sem proxy `/api` para o
backend), o uvicorn vai falhar e derrubar o preview. Nesse caso, a flag precisa
ser `false`. **Atenção:** hoje `WebBackendEnabled` é fixado em código via
`DefaultDevServerConfig()` (`internal/preview/types.go`) — não há chave de config
para ele ainda. Se precisar desligar, altere o default e rebuild, ou teste primeiro
com um repo `mobile`/`hybrid` (que não usa uvicorn). Ver `context.md §14.1`.

### 4.3 Reiniciar e verificar

```bash
sudo systemctl restart argos
journalctl -u argos -f       # acompanhe os logs
# Esperado no startup: "preview: subsistema habilitado" com port_range e timeout_min
```

---

## 5. Pré-condições de dados para o smoke test

O handler de `/preview #N` exige (ver `internal/telegram/gateway.go`):

1. **A issue N existe no SQLite do Argos** e está na fase `doing` **ou** `done`.
   Previews para issues em outras fases são recusados.
2. **O repo da issue está em checkout** em `<workspace.base_dir>/<repo>`
   (ex.: `/var/lib/argos/workspace/web`). É esse diretório que o dev server usa
   como `cwd`. Confirme que o checkout tem as deps instaladas:
   - `web`: `node_modules/` presente (rode `npm install` uma vez no diretório) e,
     se usar backend, as deps Python disponíveis.
   - `mobile`/`hybrid`: `flutter pub get` já rodado no diretório.

```bash
# inspecionar o estado do Argos
ls -la /var/lib/argos/workspace/            # checkouts dos repos
sqlite3 /var/lib/argos/argos.db \
  "SELECT id, repo, number, phase FROM issues ORDER BY id;"   # ache uma issue doing/done
```

> Se nenhuma issue estiver em `doing`/`done`, leve uma pela esteira normal
> (`agent:ready` → ... → `doing`) ou ajuste o estado para o teste. O preview é uma
> ferramenta de validação visual do trabalho já em andamento.

---

## 6. (Opcional) Conta Cloudflare + túnel nomeado

> **Não é necessário para o smoke test nem para a v1.1.** O modo rápido já entrega
> URL HTTPS funcional. Documentado aqui só para quando você quiser URL estável,
> autenticação ou domínio próprio (possível evolução futura — exigiria mudança no
> `internal/preview/tunnel.go`, que hoje só implementa o modo rápido).

1. Crie conta em <https://dash.cloudflare.com/sign-up>.
2. Adicione e verifique um domínio (ou use um já gerenciado pela Cloudflare).
3. Autentique o cloudflared na VM:
   ```bash
   cloudflared tunnel login            # abre URL p/ autorizar no browser
   cloudflared tunnel create argos-preview
   cloudflared tunnel route dns argos-preview preview.seu-dominio.com
   ```
4. Isso geraria credenciais em `~/.cloudflared/`. **Porém**, o código atual invoca
   `cloudflared tunnel --url http://localhost:<port>` (modo rápido) e ignora
   túneis nomeados — então essa configuração ficaria inerte até a feature ser
   estendida. Deixe para a v1.x se houver demanda por URL estável/autenticada.

---

## 7. Smoke test (task 4.3) — passo a passo

Mantenha `journalctl -u argos -f` aberto numa aba para correlacionar com o Telegram.

### Passo 1 — Start

- No Telegram, envie: `/preview #N` (N = issue em `doing`/`done`).
- **Esperado:**
  - Resposta imediata: "⏳ Subindo preview da issue #N (`repo`)...".
  - Em segundos a dezenas de segundos: "✅ preview pronto" com uma URL
    `https://<aleatorio>.trycloudflare.com` e o tempo de expiração (30 min).
  - Nos logs: "preview iniciado" com `issue_id`, `repo`, `url`, `expires_at`.

✅ **Critério 1** — URL HTTPS funcional entregue no Telegram.

### Passo 2 — Abrir no celular

- Abra a URL no navegador do celular (ou desktop).
- **Esperado:** a aplicação do repo carrega (React/Vite para `web`; app Flutter Web
  para `mobile`/`hybrid`).
- ⚠️ Flutter Web é aproximação visual — não substitui device físico.

### Passo 3 — Status

- Envie: `/preview status`.
- **Esperado:** lista o preview ativo com a URL e o tempo restante (< 30 min).

✅ **Critério 3** — `/preview status` lista corretamente.

### Passo 4 — Substituição (1 preview por vez)

- Com o preview ainda ativo, envie `/preview #M` para **outra** issue em `doing`/`done`.
- **Esperado:**
  - Notificação de **substituição** (preview antigo derrubado).
  - Novo preview sobe com nova URL.
  - A URL antiga para de responder.

✅ Valida `max_previews=1` + notificação de substituição.

### Passo 5 — Stop explícito

- Envie: `/preview stop #M`.
- **Esperado:**
  - Notificação de encerramento.
  - A URL para de responder.
  - Nos logs: "preview encerrado" com `reason=command`.

✅ **Critério 2** — `/preview stop` encerra servidor + túnel com confirmação.

### Passo 6 — Timeout automático

- Suba um preview e **não** o derrube. Aguarde os 30 min (ou reduza
  `timeout_minutes` para ~2 no config e reinicie, para testar mais rápido).
- **Esperado:** ao expirar, o preview é derrubado automaticamente e chega
  notificação de encerramento por timeout. A URL para de responder.

✅ **Critério 4** — timeout encerra e notifica.

### Passo 7 — Recovery no restart

- Suba um preview (deixe ativo).
- Reinicie o Argos: `sudo systemctl restart argos`.
- **Esperado:**
  - No startup, o preview transitório é marcado como `dead` e chega notificação
    de encerramento por `restart` no Telegram.
  - Nos logs: "preview marcado como dead no restart".
  - **Sem processo órfão** — confirme:
    ```bash
    pgrep -a cloudflared            # nada relacionado ao preview anterior
    pgrep -a -f 'npm run dev|flutter run|uvicorn'
    ```
  > ⚠️ Caveat conhecido: o recovery marca o registro como `dead` e notifica, mas se
  > algum processo filho sobreviveu ao kill do Argos, ele pode ficar órfão (a árvore
  > é morta via cancelamento de contexto, que não ocorre num kill abrupto). Se o
  > `pgrep` acima listar processos, mate-os manualmente e registre como achado do
  > smoke test (relevante para o critério 5).

✅ **Critério 5** — restart com preview ativo → `dead` + notificação, sem órfãos.

### Passo 8 — Build limpo (critério 6, fora da VM)

Já validado nas sessões anteriores, mas reconfirme no host de build:

```bash
CGO_ENABLED=0 go build ./... && go test ./...
```

✅ **Critério 6** — binário compila com `CGO_ENABLED=0`.

---

## 8. Checklist final dos critérios de aceite da sprint

| # | Critério | Como validar | OK? |
| - | -------- | ------------ | --- |
| 1 | `/preview #N` entrega URL HTTPS funcional | Passo 1 + 2 | ☐ |
| 2 | `/preview stop #N` encerra com confirmação | Passo 5 | ☐ |
| 3 | `/preview status` lista (URL + tempo) | Passo 3 | ☐ |
| 4 | Timeout de 30 min encerra e notifica | Passo 6 | ☐ |
| 5 | Restart → `dead` + notificação, sem órfãos | Passo 7 | ☐ |
| 6 | Build `CGO_ENABLED=0` sem warnings | Passo 8 | ☐ |
| 7 | Specs `telegram_gateway.md`/`persistence.md` atualizadas | task 4.4 (✅ feita) | ☑ |

---

## 9. Troubleshooting

| Sintoma | Causa provável | Ação |
| ------- | -------------- | ---- |
| `❌ Falha ao iniciar preview ... executable file not found in $PATH` | binário fora do PATH do systemd | §3 — drop-in de PATH |
| `❌ ... devserver: timeout após 30s` | dev server demorou (Flutter frio, `npm install`) | suba `startup_timeout_seconds`; pré-rode `npm install`/`flutter pub get` no checkout |
| `❌ ... tunnel: timeout ... aguardando URL do cloudflared` | cloudflared não emitiu URL (rede/proxy) | teste §2.5 isolado; cheque saída de rede da VM |
| uvicorn derruba o preview do `web` | SPA sem backend | §4.2 — `WebBackendEnabled=false` (hoje exige rebuild) |
| `❌ Preview so disponivel para issues em doing ou done` | issue em fase errada | §5 — use issue em `doing`/`done` |
| `❌ Issue #N nao encontrada` | issue não está no SQLite do Argos | §5 — confira a tabela `issues` |
| URL abre mas app não carrega | dev server respondeu no healthcheck mas asset/proxy quebrou | veja logs do dev server em `journalctl -u argos` (linhas com `process=...`) |
| Processo órfão após restart | kill abrupto não propagou SIGTERM à árvore | mate via `pgrep`; registrar como achado (critério 5) |

---

## 10. Ao concluir

- Marque os critérios no §8.
- Atualize `docs/specs/sprints/preview-v1.1/progress.md`: task **4.3 → ✅** e a Fase 4
  como totalmente concluída; adicione entrada no log de sessões com os achados
  (especialmente sobre órfãos/timeout, se houver).
- Se algum ponto aberto (§14.2 startup timeout, §14.3 sinal de término) foi
  resolvido na prática, registre a decisão em `context.md`.
