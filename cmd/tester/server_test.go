package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"tester_mm/internal/events"
	"tester_mm/internal/mmlink"
)

func always(string) (bool, error) { return true, nil }

// newTestServer создаёт сервер без сети: подключение к MM заменено функцией, которая запоминает параметры из Connect
func newTestServer() (*sswServer, *events.Bus, *[]mmlink.Params) {
	var established []mmlink.Params

	link := mmlink.New(func(_ context.Context, params mmlink.Params) (*mmlink.Session, error) {
		established = append(established, params)

		return &mmlink.Session{ID: params.SessionID}, nil
	})

	bus := events.NewBus()

	return &sswServer{link: link, events: bus}, bus, &established
}

func TestHandleRequestPublishesEvent(t *testing.T) {
	server, bus, _ := newTestServer()

	// в логе параметры без нулевых полей, а в событии — полные
	resp, err := server.handleRequest("mm.client_api.EventService", "AudioFilePlayed",
		[]byte(`{"channel_id":"7"}`), []byte(`{"channel_id":"7","player_id":0}`))
	if err != nil || resp != nil {
		t.Fatalf("handleRequest: resp=%v err=%v", resp, err)
	}

	got, err := bus.Wait("AudioFilePlayed", always, 0)
	if err != nil || got != `{"channel_id":"7","player_id":0}` {
		t.Errorf("событие в буфере: %q, ошибка %v", got, err)
	}
}

func TestHandleConnectPassesParamsToLink(t *testing.T) {
	server, bus, established := newTestServer()

	resp, err := server.handleRequest("svc", "Connect",
		[]byte(`{"session_id":"s1","server_ip":"127.0.0.1","server_port":7001}`), nil)
	if err != nil || resp["reply_code"] != int32(0) {
		t.Fatalf("handleRequest(Connect): resp=%v err=%v", resp, err)
	}

	// повторный Connect с другими данными игнорируется, но MM получает обычный успешный ответ
	resp, err = server.handleRequest("svc", "Connect",
		[]byte(`{"session_id":"s2","server_ip":"10.0.0.1","server_port":9999}`), nil)
	if err != nil || resp["reply_code"] != int32(0) {
		t.Fatalf("повторный Connect: resp=%v err=%v", resp, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	session, err := server.link.Ensure(ctx)
	if err != nil || session.ID != "s1" {
		t.Fatalf("Ensure: session=%+v err=%v", session, err)
	}

	want := mmlink.Params{SessionID: "s1", Address: "127.0.0.1", Port: 7001}
	if len(*established) != 1 || (*established)[0] != want {
		t.Errorf("подключение по параметрам %+v, ожидалось один раз %+v", *established, want)
	}

	// Connect — не событие
	if _, err := bus.Wait("Connect", always, 0); err == nil {
		t.Error("Connect не должен попадать в события")
	}
}

func TestHandleConnectRejectsMissingAddress(t *testing.T) {
	server, _, _ := newTestServer()

	for _, body := range []string{`{"session_id":"s1"}`, `{"session_id":"s1","server_ip":"127.0.0.1"}`, `{"session_id":"s1","server_port":7001}`, `не json`} {
		if _, err := server.handleRequest("svc", "Connect", []byte(body), nil); err == nil {
			t.Errorf("%s: ожидалась ошибка", body)
		}
	}
}

func TestHandleConnectErrorMessage(t *testing.T) {
	server, _, _ := newTestServer()

	_, err := server.handleRequest("svc", "Connect", []byte(`{"session_id":"s1"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "server_ip или server_port") {
		t.Errorf("сообщение об ошибке: %v", err)
	}
}
