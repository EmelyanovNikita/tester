package mmlink

import (
	"context"
	"net"
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

func loadVerificationProto(t *testing.T) *protoloader.ProtoData {
	t.Helper()

	data, err := protoloader.LoadProto("testdata", "verification.proto")
	if err != nil {
		t.Fatalf("LoadProto: %v", err)
	}

	return data
}

// startVerificationServer поднимает в памяти сервер с методом CredentialsSend и запоминает полученный запрос
func startVerificationServer(t *testing.T, data *protoloader.ProtoData) (*grpc.ClientConn, func() (sessionID, instance string, calls int)) {
	t.Helper()

	info := data.Methods["CredentialsSend"]

	var (
		mu                  sync.Mutex
		sessionID, instance string
		calls               int
	)

	handler := func(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
		req := dynamic.NewMessage(info.Request)
		if err := dec(req); err != nil {
			return nil, err
		}

		mu.Lock()
		defer mu.Unlock()

		sessionID, _ = req.GetFieldByName("session_id").(string)
		instance, _ = req.GetFieldByName("instance").(string)
		calls++

		return dynamic.NewMessage(info.Response), nil
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

	return conn, func() (string, string, int) {
		mu.Lock()
		defer mu.Unlock()

		return sessionID, instance, calls
	}
}

func TestAuthenticateSendsSessionAndInstance(t *testing.T) {
	data := loadVerificationProto(t)
	conn, received := startVerificationServer(t, data)

	if err := Authenticate(context.Background(), conn, data.Methods, "abc123"); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	sessionID, instance, calls := received()
	if sessionID != "abc123" || instance != "tester" || calls != 1 {
		t.Errorf("сервер получил session_id=%q instance=%q (вызовов: %d), ожидалось abc123/tester/1", sessionID, instance, calls)
	}
}

func TestAuthenticateWithoutProtoMethod(t *testing.T) {
	err := Authenticate(context.Background(), nil, map[string]protoloader.MethodInfo{}, "abc")
	if err == nil || !strings.Contains(err.Error(), "CredentialsSend не найден") {
		t.Fatalf("ожидалась ошибка «CredentialsSend не найден», получили: %v", err)
	}
}

func TestDialConnects(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	server := grpc.NewServer()

	go func() {
		_ = server.Serve(lis)
	}()

	t.Cleanup(server.Stop)

	port := lis.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := Dial(ctx, "127.0.0.1", port)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	_ = conn.Close()
}

// Пока MM недоступен, Dial повторяет попытки, но отмена контекста его останавливает
func TestDialStopsOnCancel(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close() // порт свободен, подключиться некуда

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()

	_, err = Dial(ctx, "127.0.0.1", port)
	if err == nil || !strings.Contains(err.Error(), "прервано") {
		t.Fatalf("ожидалась ошибка «прервано», получили: %v", err)
	}

	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Dial не остановился по отмене контекста: %v", elapsed)
	}
}
