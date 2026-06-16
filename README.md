# Argos

Orquestrador em Go que gerencia agentes de codificação autônomos. Faz polling de GitHub Issues, despacha tarefas ao Claude Code CLI, monitora uso de janela de contexto e usa Telegram como canal de aprovação humana.

## Pré-requisitos na VM

- Go 1.22+
- git
- Claude Code CLI (`claude`)
- Caddy (para TLS + reverse proxy)
- systemd

## Instalação

1. Clone o repositório na VM:

```
git clone https://github.com/<org>/argos /opt/argos
cd /opt/argos
```

2. Compile o binário estático (sem CGO):

```
CGO_ENABLED=0 go build -o argos ./cmd/orchestrator
```

3. Copie o binário para o PATH do sistema:

```
cp argos /usr/local/bin/argos
```

4. Crie o diretório de configuração e dados:

```
mkdir -p /etc/argos /var/lib/argos
```

5. Copie e preencha a configuração e as variáveis de ambiente:

```
cp config.example.yaml /etc/argos/config.yaml
cp deploy/env.example /etc/argos/env
# Edite /etc/argos/config.yaml e /etc/argos/env com os valores reais.
# /etc/argos/env deve ter permissão 600 (contém tokens).
chmod 600 /etc/argos/env
```

6. Instale e ative o serviço systemd:

```
cp deploy/argos.service /etc/systemd/system/argos.service
systemctl daemon-reload
systemctl enable --now argos
```

7. Instale o Caddyfile e recarregue o Caddy:

```
# Edite deploy/Caddyfile com o domínio real antes de copiar.
cp deploy/Caddyfile /etc/caddy/Caddyfile
systemctl reload caddy
```

## Configuração

- `/etc/argos/config.yaml` — referência completa em `config.example.yaml`.
- `/etc/argos/env` — variáveis obrigatórias: `GITHUB_TOKEN`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_WEBHOOK_SECRET`.

## Operação

```
# Status do serviço
systemctl status argos

# Reiniciar após atualização
systemctl restart argos

# Parar
systemctl stop argos

# Logs em tempo real
journalctl -u argos -f
```

Comandos Telegram disponíveis:

| Comando             | Efeito                                         |
| ------------------- | ---------------------------------------------- |
| `/status`           | Status de todos os repos e issues em andamento |
| `/approve #N`       | Aprova a spec da issue N                       |
| `/reject #N motivo` | Rejeita a spec e envia o motivo ao agente      |
| `/pause repo`       | Pausa o polling do repo                        |
| `/resume repo`      | Retoma o polling do repo                       |
| `/run #N`           | Força o processamento imediato da issue N      |

## Atualizar

```
cd /opt/argos
git pull
CGO_ENABLED=0 go build -o /usr/local/bin/argos ./cmd/orchestrator
systemctl restart argos
```
