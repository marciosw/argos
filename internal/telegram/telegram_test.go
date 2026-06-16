package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marciomacedo/argos/internal/config"
	"github.com/marciomacedo/argos/internal/domain"
	"github.com/marciomacedo/argos/internal/store"
)

// ======================= parseCommand =======================

func TestParseCommandApproveHash(t *testing.T) {
	cmd, err := parseCommand("/approve #42", 100)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Type != domain.CmdApprove || cmd.Issue != 42 || cmd.ChatID != 100 {
		t.Fatalf("got %+v", cmd)
	}
}

func TestParseCommandApproveNoHash(t *testing.T) {
	cmd, err := parseCommand("/approve 42", 100)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Type != domain.CmdApprove || cmd.Issue != 42 {
		t.Fatalf("got %+v", cmd)
	}
}

func TestParseCommandApproveBotName(t *testing.T) {
	cmd, err := parseCommand("/approve@argos_bot #42", 100)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Type != domain.CmdApprove || cmd.Issue != 42 {
		t.Fatalf("got %+v", cmd)
	}
}

func TestParseCommandRejectLongReason(t *testing.T) {
	cmd, err := parseCommand("/reject #1 texto longo com espacos", 100)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Type != domain.CmdReject || cmd.Issue != 1 {
		t.Fatalf("got %+v", cmd)
	}
	if cmd.Reason != "texto longo com espacos" {
		t.Fatalf("reason = %q", cmd.Reason)
	}
}

func TestParseCommandRejectMissingReason(t *testing.T) {
	_, err := parseCommand("/reject #1", 100)
	if err == nil {
		t.Fatal("esperado erro por motivo ausente")
	}
}

func TestParseCommandPauseValid(t *testing.T) {
	cmd, err := parseCommand("/pause web", 100)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Type != domain.CmdPause || cmd.Repo != "web" {
		t.Fatalf("got %+v", cmd)
	}
}

func TestParseCommandPauseInvalidRepo(t *testing.T) {
	_, err := parseCommand("/pause invalid", 100)
	if err == nil {
		t.Fatal("esperado erro para repo invalido")
	}
	if !strings.Contains(err.Error(), "invalido") {
		t.Fatalf("mensagem de erro inesperada: %v", err)
	}
}

func TestParseCommandStatus(t *testing.T) {
	cmd, err := parseCommand("/status", 100)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Type != domain.CmdStatus || cmd.ChatID != 100 {
		t.Fatalf("got %+v", cmd)
	}
}

func TestParseCommandUnknown(t *testing.T) {
	_, err := parseCommand("/foo", 100)
	if err == nil {
		t.Fatal("esperado erro para comando desconhecido")
	}
	if !strings.Contains(err.Error(), "desconhecido") {
		t.Fatalf("mensagem de erro inesperada: %v", err)
	}
}

// ======================= FormatDigest =======================

func TestFormatDigest(t *testing.T) {
	d := DigestData{
		IssueRepo:  "web",
		IssueNum:   42,
		IssueTitle: "Titulo da issue",
		Objectives: "Objetivo principal",
		Components: "internal/foo",
		Decisions:  "Decisao A",
		Risks:      "Risco de migracao",
	}
	out := FormatDigest(d)
	checks := []string{
		"#42", "web", "Titulo da issue",
		"Objetivo principal", "internal/foo",
		"Decisao A", "Risco de migracao",
		"/approve #42", "/reject #42",
	}
	for _, s := range checks {
		if !strings.Contains(out, s) {
			t.Errorf("FormatDigest: esperado %q em:\n%s", s, out)
		}
	}
}

// ======================= tgClient (sendMessage / sendDocument) =======================

func newTestClient(t *testing.T, handler http.Handler) *tgClient {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &tgClient{
		base:     ts.URL,
		botToken: "test-token",
		http:     ts.Client(),
	}
}

func TestSendMessagePath(t *testing.T) {
	var gotPath string
	var gotBody map[string]any

	tg := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))

	if err := tg.sendMessage(context.Background(), 123, "ola", "HTML"); err != nil {
		t.Fatal(err)
	}

	wantPath := "/bottest-token/sendMessage"
	if gotPath != wantPath {
		t.Errorf("path = %q, quer %q", gotPath, wantPath)
	}
	if gotBody["chat_id"].(float64) != 123 {
		t.Errorf("chat_id = %v", gotBody["chat_id"])
	}
	if gotBody["text"] != "ola" {
		t.Errorf("text = %v", gotBody["text"])
	}
	if gotBody["parse_mode"] != "HTML" {
		t.Errorf("parse_mode = %v", gotBody["parse_mode"])
	}
}

func TestSendDocumentPath(t *testing.T) {
	var gotPath string
	var gotChatID string
	var gotCaption string
	var gotFileContent []byte
	var gotFilename string

	tg := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path

		ct := r.Header.Get("Content-Type")
		_, params, _ := mime.ParseMediaType(ct)
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("multipart: %v", err)
				break
			}
			data, _ := io.ReadAll(part)
			switch part.FormName() {
			case "chat_id":
				gotChatID = string(data)
			case "caption":
				gotCaption = string(data)
			case "document":
				gotFilename = part.FileName()
				gotFileContent = data
			}
		}
		w.WriteHeader(http.StatusOK)
	}))

	content := []byte("conteudo do arquivo")
	if err := tg.sendDocument(context.Background(), 456, "spec.md", content, "spec.md"); err != nil {
		t.Fatal(err)
	}

	wantPath := "/bottest-token/sendDocument"
	if gotPath != wantPath {
		t.Errorf("path = %q, quer %q", gotPath, wantPath)
	}
	if gotChatID != "456" {
		t.Errorf("chat_id = %q", gotChatID)
	}
	if gotCaption != "spec.md" {
		t.Errorf("caption = %q", gotCaption)
	}
	if gotFilename != "spec.md" {
		t.Errorf("filename = %q", gotFilename)
	}
	if !bytes.Equal(gotFileContent, content) {
		t.Errorf("conteudo divergente")
	}
}

// ======================= webhook handler =======================

func openMemoryStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newTestGateway(t *testing.T, cmds chan<- domain.Command, apiHandler http.Handler) (*gateway, *httptest.Server) {
	t.Helper()
	apiServer := httptest.NewServer(apiHandler)
	t.Cleanup(apiServer.Close)

	cfg := &config.TelegramConfig{
		BotTokenEnv:    "unused",
		SecretTokenEnv: "unused",
		AllowedChatIDs: []int64{999},
		WebhookURL:     "",
	}
	st := openMemoryStore(t)
	gw := newWithToken(cfg, st, cmds, "tok", "mysecret", apiServer.URL)
	gw.tg.http = apiServer.Client()
	return gw, apiServer
}

func postWebhook(t *testing.T, gw *gateway, secret string, update tgUpdate) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(update)
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	}
	rr := httptest.NewRecorder()
	gw.webhookHandler(rr, req)
	return rr
}

func makeUpdate(updateID, chatID int64, text string) tgUpdate {
	return tgUpdate{
		UpdateID: updateID,
		Message:  &tgMessage{Chat: tgChat{ID: chatID}, Text: text},
	}
}

func TestWebhookInvalidSecret(t *testing.T) {
	cmds := make(chan domain.Command, 1)
	gw, _ := newTestGateway(t, cmds, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := postWebhook(t, gw, "wrong-secret", makeUpdate(1, 999, "/status"))
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, quer 403", rr.Code)
	}
}

func TestWebhookChatNotAllowed(t *testing.T) {
	cmds := make(chan domain.Command, 1)
	gw, _ := newTestGateway(t, cmds, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// chatID=888 não está em AllowedChatIDs (que tem só 999)
	rr := postWebhook(t, gw, "mysecret", makeUpdate(1, 888, "/status"))
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, quer 200", rr.Code)
	}
	// nada deve ter chegado ao canal
	if len(cmds) != 0 {
		t.Error("comando nao deveria ter sido enviado para chat nao autorizado")
	}
}

func TestWebhookDuplicateUpdateID(t *testing.T) {
	cmds := make(chan domain.Command, 2)
	gw, _ := newTestGateway(t, cmds, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	update := makeUpdate(42, 999, "/approve #7")

	rr := postWebhook(t, gw, "mysecret", update)
	if rr.Code != http.StatusOK {
		t.Errorf("primeira requisicao: status = %d", rr.Code)
	}

	// Segunda vez com mesmo update_id — deve ser descartada
	rr2 := postWebhook(t, gw, "mysecret", update)
	if rr2.Code != http.StatusOK {
		t.Errorf("segunda requisicao: status = %d", rr2.Code)
	}

	// Aguardar o primeiro comando chegar e confirmar que não chegou segundo
	select {
	case <-cmds:
		// primeiro comando recebido corretamente
	case <-time.After(2 * time.Second):
		t.Fatal("comando do primeiro update nao chegou ao canal")
	}

	// Dar tempo ao segundo update (descartado) — não deve depositar nada
	time.Sleep(50 * time.Millisecond)
	if len(cmds) != 0 {
		t.Errorf("update duplicado nao deveria gerar segundo comando")
	}
}

func TestWebhookValidCommandSentToChannel(t *testing.T) {
	cmds := make(chan domain.Command, 1)
	gw, _ := newTestGateway(t, cmds, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := postWebhook(t, gw, "mysecret", makeUpdate(10, 999, "/approve #5"))
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d", rr.Code)
	}

	var cmd domain.Command
	select {
	case cmd = <-cmds:
	case <-time.After(2 * time.Second):
		t.Fatal("comando nao chegou ao canal")
	}
	if cmd.Type != domain.CmdApprove || cmd.Issue != 5 || cmd.ChatID != 999 {
		t.Errorf("comando inesperado: %+v", cmd)
	}
}
