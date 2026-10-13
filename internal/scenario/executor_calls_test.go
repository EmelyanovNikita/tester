package scenario

import (
	"context"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"tester_mm/internal/protoloader"

	"github.com/jhump/protoreflect/dynamic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// startEchoServer поднимает в памяти gRPC-сервер с методом test.TestService/Echo.
// Echo отвечает тем же channel_id; на запрос с channel_id == failOn возвращает reply_code = RC_FAIL.
// Вторым значением возвращается функция, отдающая channel_id всех полученных запросов по порядку.
func startEchoServer(t *testing.T, data *protoloader.ProtoData, failOn uint64) (*grpc.ClientConn, func() []uint64) {
	t.Helper()

	info := data.Methods["Echo"]

	var (
		mu   sync.Mutex
		seen []uint64
	)

	handler := func(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
		req := dynamic.NewMessage(info.Request)
		if err := dec(req); err != nil {
			return nil, err
		}

		id, _ := req.GetFieldByName("channel_id").(uint64)

		mu.Lock()
		seen = append(seen, id)
		mu.Unlock()

		resp := dynamic.NewMessage(info.Response)
		resp.SetFieldByName("channel_id", id)

		if id == failOn {
			resp.SetFieldByName("reply_code", int32(1)) // RC_FAIL
		}

		return resp, nil
	}

	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: info.Service,
		HandlerType: (*interface{})(nil),
		Methods:     []grpc.MethodDesc{{MethodName: info.Method, Handler: handler}},
	}, nil)

	lis := bufconn.Listen(1 << 20)

	go func() {
		_ = server.Serve(lis)
	}()

	conn, err := grpc.DialContext(
		context.Background(),
		"bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("подключение к тестовому серверу: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		server.Stop()
	})

	return conn, func() []uint64 {
		mu.Lock()
		defer mu.Unlock()

		return append([]uint64(nil), seen...)
	}
}

const loopWithCalls = "for i in 1..3 {\n    resp = Echo(SESSION_ID, i)\n    sleep(0.01)\n}\n"

func TestForWithCalls(t *testing.T) {
	data := loadTestProto(t)
	conn, seen := startEchoServer(t, data, 0)

	executor := NewExecutor(conn, data.Methods, "sess1", data.Enums)

	if err := executor.Execute(mustParse(t, data, loopWithCalls)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got, want := seen(), []uint64{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("сервер получил channel_id %v, ожидалось %v", got, want)
	}

	// ответ последнего вызова доступен как переменная, в том числе поле channel_id
	if got, _ := varValue(executor, "resp"); !strings.Contains(got, `"channel_id":"3"`) {
		t.Errorf("в resp нет channel_id=3: %s", got)
	}
}

func TestForStopsOnReplyCode(t *testing.T) {
	data := loadTestProto(t)
	conn, seen := startEchoServer(t, data, 2)

	executor := NewExecutor(conn, data.Methods, "sess1", data.Enums)

	err := executor.Execute(mustParse(t, data, loopWithCalls))
	if err == nil {
		t.Fatal("ожидалась ошибка: на втором запросе сервер вернул RC_FAIL")
	}

	if !strings.Contains(err.Error(), "RC_FAIL") || !strings.Contains(err.Error(), "команде 1.1") {
		t.Errorf("в ошибке нет RC_FAIL или номера команды 1.1: %v", err)
	}

	if got, want := seen(), []uint64{1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("сервер получил channel_id %v, ожидалось %v: цикл должен остановиться на ошибке", got, want)
	}
}

func TestForContinueOnReplyCode(t *testing.T) {
	data := loadTestProto(t)
	conn, seen := startEchoServer(t, data, 2)

	executor := NewExecutor(conn, data.Methods, "sess1", data.Enums)
	executor.ContinueOnError = true

	err := executor.Execute(mustParse(t, data, loopWithCalls))
	if err == nil || !strings.Contains(err.Error(), "команд с ошибками: 1 из 6") {
		t.Fatalf("ожидалась ошибка «команд с ошибками: 1 из 6», получили: %v", err)
	}

	if got, want := seen(), []uint64{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("сервер получил channel_id %v, ожидалось %v", got, want)
	}
}

// Статус ответа можно проверить в сценарии: с ContinueOnError ответ с RC_FAIL сохраняется в переменную,
// а if сравнивает resp.reply_code (в JSON ответа это имя enum) с константой RC_FAIL
func TestIfChecksReplyCode(t *testing.T) {
	data := loadTestProto(t)
	conn, seen := startEchoServer(t, data, 2)

	executor := NewExecutor(conn, data.Methods, "sess1", data.Enums)
	executor.ContinueOnError = true

	script := `
for i in 1..3 {
    resp = Echo(SESSION_ID, i)
    if resp.reply_code == RC_FAIL {
        failed_at = i
        log("ошибка на шаге", i, "код", resp.reply_code)
    }
}
`

	err := executor.Execute(mustParse(t, data, script))
	if err == nil || !strings.Contains(err.Error(), "команд с ошибками: 1 из") {
		t.Fatalf("ожидалась ошибка «команд с ошибками: 1 из ...», получили: %v", err)
	}

	if got, want := seen(), []uint64{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("сервер получил channel_id %v, ожидалось %v", got, want)
	}

	if got, _ := varValue(executor, "failed_at"); got != "2" {
		t.Errorf("failed_at = %q, ожидалось \"2\": if не сработал на ответе с RC_FAIL", got)
	}
}

// Вызов метода до подключения — понятная ошибка, а не паника на пустом соединении
func TestCallWithoutConnection(t *testing.T) {
	data := loadTestProto(t)

	executor := NewExecutor(nil, data.Methods, "", data.Enums)

	err := executor.Execute(mustParse(t, data, "resp = Echo(\"x\", 1)\n"))
	if err == nil || !strings.Contains(err.Error(), "нет соединения с MM") {
		t.Fatalf("ожидалась ошибка «нет соединения с MM», получили: %v", err)
	}
}

// connect() в сценарии подключает исполнитель, после чего вызовы методов идут через полученное соединение
func TestConnectThenCall(t *testing.T) {
	data := loadTestProto(t)
	conn, seen := startEchoServer(t, data, 0)

	executor := NewExecutor(nil, data.Methods, "", data.Enums)
	executor.SetConnector(func() (*grpc.ClientConn, string, error) {
		return conn, "sess-from-connect", nil
	})

	if err := executor.Execute(mustParse(t, data, "connect()\nresp = Echo(SESSION_ID, 7)\nconnect()\n")); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got, want := seen(), []uint64{7}; !reflect.DeepEqual(got, want) {
		t.Errorf("сервер получил channel_id %v, ожидалось %v", got, want)
	}

	if got, _ := varValue(executor, "SESSION_ID"); got != "sess-from-connect" {
		t.Errorf("SESSION_ID = %q", got)
	}
}

func TestPauseUsesDurationFlag(t *testing.T) {
	parser := NewParser(nil)

	parsed, err := parser.Parse("pause()\npause(30)\n")
	if err != nil {
		t.Fatal(err)
	}

	executor := NewExecutor(nil, nil, "", nil)
	executor.SetQuiet(true)
	executor.SetPause(60)

	begin := time.Now()
	if err := executor.Execute(parsed); err != nil {
		t.Fatal(err)
	}

	if took := time.Since(begin); took < 85*time.Millisecond || took > 400*time.Millisecond {
		t.Errorf("pause() 60 мс + pause(30) заняли %v, ожидалось около 90 мс", took)
	}
}

func TestPauseWithoutDurationIsSkipped(t *testing.T) {
	parsed, err := NewParser(nil).Parse("pause()\n")
	if err != nil {
		t.Fatal(err)
	}

	executor := NewExecutor(nil, nil, "", nil)
	executor.SetQuiet(true)

	begin := time.Now()
	if err := executor.Execute(parsed); err != nil || time.Since(begin) > 50*time.Millisecond {
		t.Errorf("pause() без -d должна пропускаться: %v", err)
	}
}

func TestPauseRejectsBadSyntax(t *testing.T) {
	if _, err := NewParser(nil).Parse("pause 5\n"); err == nil {
		t.Error("ожидалась ошибка синтаксиса")
	}
}
