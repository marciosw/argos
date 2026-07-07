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

# Orientações para configuração do instagram

## Como Cadastrar Webhook no Telegram

Cadastrar um webhook no Telegram é um processo simples. Você só precisa do **token de API do seu bot** e da **URL do seu servidor/aplicação** (que deve obrigatoriamente usar HTTPS). Basta enviar uma requisição HTTP via navegador ou terminal para conectar os dois.

### Passo 1: Obter o Token do Bot

1. Abra o Telegram e procure pelo **[BotFather](https://t.me)**.
2. Envie o comando `/newbot` e siga as instruções para criar um nome e um usuário.
3. O **BotFather** fornecerá um **Token de Acesso** (ex: `123456789:ABCDefGhIJKlmNoPQRsTUVwxyZ`). Guarde-o com segurança.

### Passo 2: Cadastrar o Webhook

Abra o seu navegador de internet ou terminal de preferência e acesse a URL abaixo, substituindo os valores pelas suas informações:

```text
https://telegram.org{SEU_TOKEN}/setWebhook?url={URL_DO_SEU_SERVIDOR}
```

#### Exemplo prático:
`https://telegram.org123456789:ABCDefGhIJKlmNoPQRsTUVwxyZ/setWebhook?url=https://meuservidor.com/webhook`

Se a configuração for bem-sucedida, você receberá uma resposta JSON confirmando:
```json
{
  "ok": true,
  "result": true,
  "description": "Webhook was set"
}
```

### Como Verificar ou Remover o Webhook

* **Verificar status:** Acesse `https://telegram.org{SEU_TOKEN}/getWebhookInfo` no navegador para checar se o webhook está ativo e se há erros de envio.
* **Remover webhook:** Acesse `https://telegram.org{SEU_TOKEN}/setWebhook?url=` (deixando o campo `url` vazio) para cancelar o envio automático e voltar para o sistema manual de consultas (polling).
